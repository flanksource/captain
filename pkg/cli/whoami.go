package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
)

type WhoamiOptions struct {
	Backend string `flag:"backend" help:"Show only this backend: anthropic|openai|gemini|deepseek|claude-cli|claude-agent|claude-cmux|codex-cli|codex-agent|codex-cmux|gemini-cli" short:"b"`
	Models  bool   `flag:"models" help:"Probe each provider's models endpoint via a live API call" default:"true" short:"m"`
	Limit   int    `flag:"limit" help:"Max sample model IDs to show per adapter in pretty output after per-prefix filtering (0 = all)" default:"0" short:"l"`
}

// AdapterStatus is the resolved auth/availability of a single agent adapter
// (backend). Type is "api" for HTTP providers called with a key, "cli" for
// backends delegated to an installed coding-agent binary.
type AdapterStatus struct {
	Backend       string   `json:"backend"`
	Type          string   `json:"type"`
	Authenticated bool     `json:"authenticated"`
	AuthMethod    string   `json:"authMethod,omitempty"`
	AuthDetail    string   `json:"authDetail,omitempty"`
	Binary        string   `json:"binary,omitempty"`
	BinaryMissing string   `json:"binaryMissing,omitempty"`
	ModelCount    int      `json:"modelCount"`
	Models        []string `json:"models,omitempty"`
	ModelError    string   `json:"modelError,omitempty"`

	ModelDetails []ai.ModelDef `json:"-"`
}

// Ready reports whether the adapter can actually run: authenticated, and (for
// CLI backends) with its binary present in PATH.
func (a AdapterStatus) Ready() bool {
	if !a.Authenticated {
		return false
	}
	if a.Type == "cli" {
		return a.Binary != ""
	}
	return true
}

type WhoamiResult struct {
	Adapters []AdapterStatus `json:"adapters"`

	// Display-only knobs for Pretty(); never serialized.
	sampleLimit int
	showModels  bool
}

// authProbe abstracts the host environment (env vars, PATH, credential files)
// so resolveAdapter stays pure and testable.
type authProbe struct {
	getenv     func(string) string
	lookPath   func(string) (string, error)
	fileExists func(string) bool
	home       string
}

func osAuthProbe() authProbe {
	home, _ := os.UserHomeDir()
	return authProbe{
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		fileExists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		home: home,
	}
}

// loginFile is a credential file whose presence indicates a CLI has been logged
// in out-of-band (subscription/OAuth) rather than via an API-key env var.
type loginFile struct {
	rel   string // path relative to the user's home directory
	label string // human label, e.g. "codex login"
}

// cliAdapter holds the CLI-only metadata for a backend: the binary that must be
// on PATH and the credential files that signal a completed login.
type cliAdapter struct {
	binary string
	logins []loginFile
}

func cliAdapters() map[ai.Backend]cliAdapter {
	claude := cliAdapter{
		binary: "claude",
		logins: []loginFile{
			{rel: filepath.Join(".claude", ".credentials.json"), label: "claude login"},
			{rel: ".claude.json", label: "claude login"},
		},
	}
	return map[ai.Backend]cliAdapter{
		ai.BackendClaudeAgent: claude,
		ai.BackendClaudeCLI:   claude,
		ai.BackendClaudeCmux:  claude,
		ai.BackendCodexCLI: {
			binary: "codex",
			logins: []loginFile{{rel: filepath.Join(".codex", "auth.json"), label: "codex login"}},
		},
		ai.BackendCodexAgent: {
			binary: "codex",
			logins: []loginFile{{rel: filepath.Join(".codex", "auth.json"), label: "codex login"}},
		},
		ai.BackendCodexCmux: {
			binary: "codex",
			logins: []loginFile{{rel: filepath.Join(".codex", "auth.json"), label: "codex login"}},
		},
		ai.BackendGeminiCLI: {
			binary: "gemini",
			logins: []loginFile{
				{rel: filepath.Join(".gemini", "oauth_creds.json"), label: "gemini login"},
				{rel: filepath.Join(".gemini", "google_accounts.json"), label: "gemini login"},
			},
		},
	}
}

// resolveAdapter determines a backend's auth method and (for CLI backends)
// binary availability from the probed environment. An API-key env var always
// wins over a CLI login file because that is the path NewProvider/ListModels
// actually take.
func resolveAdapter(backend ai.Backend, p authProbe) AdapterStatus {
	st := AdapterStatus{Backend: string(backend), Type: backend.Kind()}

	for _, v := range ai.AuthEnvVars(backend) {
		if val := p.getenv(v); strings.TrimSpace(val) != "" {
			st.Authenticated = true
			st.AuthMethod = v + " (env)"
			st.AuthDetail = maskKey(val)
			break
		}
	}

	if cli, ok := cliAdapters()[backend]; ok {
		if path, err := p.lookPath(cli.binary); err == nil {
			st.Binary = path
		} else {
			st.BinaryMissing = cli.binary
		}
		if !st.Authenticated {
			for _, lf := range cli.logins {
				full := filepath.Join(p.home, lf.rel)
				if p.fileExists(full) {
					st.Authenticated = true
					st.AuthMethod = lf.label
					st.AuthDetail = full
					break
				}
			}
		}
	}

	return st
}

// maskKey renders a secret as the first and last four characters so the output
// is identifiable without ever exposing the full token.
func maskKey(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

func firstEnv(vars []string, getenv func(string) string) string {
	for _, v := range vars {
		if val := getenv(v); strings.TrimSpace(val) != "" {
			return val
		}
	}
	return ""
}

func RunWhoami(opts WhoamiOptions) (any, error) {
	adapters, err := ProbeAdapters(opts, osAuthProbe())
	if err != nil {
		return nil, err
	}
	return WhoamiResult{Adapters: adapters, sampleLimit: opts.Limit, showModels: opts.Models}, nil
}

// ProbeAdapters resolves each backend's auth/availability and (when opts.Models)
// its model listing against the supplied environment probe. It is the shared,
// injectable core behind both `captain whoami` and the prompt --schema builder,
// so passing a stub authProbe keeps callers hermetic (no live API calls when the
// probe reports no API keys).
func ProbeAdapters(opts WhoamiOptions, probe authProbe) ([]AdapterStatus, error) {
	backends := ai.AllBackends()
	if opts.Backend != "" {
		b := ai.Backend(opts.Backend)
		if !b.Valid() {
			return nil, fmt.Errorf("--backend must be one of: %s (got %q)", ai.BackendList(), opts.Backend)
		}
		backends = []ai.Backend{b}
	}

	var models map[ai.Backend]modelFetch
	if opts.Models {
		models = fetchAPIModels(backends, probe.getenv)
	}

	adapters := make([]AdapterStatus, 0, len(backends))
	for _, b := range backends {
		st := resolveAdapter(b, probe)
		if opts.Models {
			applyModels(&st, b, models, probe.getenv)
		}
		adapters = append(adapters, st)
	}
	return adapters, nil
}

type modelFetch struct {
	models []ai.ModelDef
	err    error
}

var resolveModelRows = ai.ResolveModels

// fetchAPIModels resolves each provider's live /v1/models endpoint once,
// concurrently. The resolver is Captain's cached model path, so repeated whoami
// calls reuse a fresh cache instead of hitting providers every time. CLI/agent
// backends are mapped to their parent API provider before fetching.
func fetchAPIModels(backends []ai.Backend, getenv func(string) string) map[ai.Backend]modelFetch {
	apis := map[ai.Backend]bool{}
	for _, b := range backends {
		source := modelSourceBackend(b)
		if source == "" {
			continue
		}
		if firstEnv(ai.AuthEnvVars(source), getenv) != "" {
			apis[source] = true
		}
	}

	out := make(map[ai.Backend]modelFetch, len(apis))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for b := range apis {
		wg.Add(1)
		go func(backend ai.Backend) {
			defer wg.Done()
			rows, err := resolveModelRows(context.Background(), ai.ResolveOptions{Backend: backend, UseTokens: true})
			m := liveModelDefs(rows, backend)
			mu.Lock()
			out[backend] = modelFetch{models: m, err: err}
			mu.Unlock()
		}(b)
	}
	wg.Wait()
	return out
}

func liveModelDefs(rows []ai.ResolvedModel, backend ai.Backend) []ai.ModelDef {
	out := make([]ai.ModelDef, 0, len(rows))
	for _, row := range rows {
		if !row.Live {
			continue
		}
		id := row.RuntimeID()
		if id == "" {
			continue
		}
		name := row.Label
		if name == "" {
			name = id
		}
		out = append(out, ai.ModelDef{ID: id, Name: name, Backend: backend, ReleaseDate: row.ReleaseDate})
	}
	return out
}

// applyModels fills in the model listing (or the reason it is unavailable) for
// a single adapter. All backends use live provider model rows from Captain's
// cached resolver; CLI/agent backends map those provider rows into the runtime
// model slugs their local binaries accept.
func applyModels(st *AdapterStatus, b ai.Backend, cache map[ai.Backend]modelFetch, getenv func(string) string) {
	source := modelSourceBackend(b)
	if source == "" {
		st.ModelError = fmt.Sprintf("backend %s has no model listing", b)
		return
	}

	envVars := ai.AuthEnvVars(source)
	if firstEnv(envVars, getenv) == "" {
		if b.Kind() == "cli" {
			setModels(st, ai.RegistryModelDefs(b))
			return
		}
		st.ModelError = "set " + strings.Join(envVars, " or ") + " to list models"
		return
	}

	fetch, ok := cache[source]
	if !ok {
		return
	}
	if fetch.err != nil {
		st.ModelError = fetch.err.Error()
		return
	}
	setModels(st, modelsForAdapterBackend(b, fetch.models))
}

func modelSourceBackend(backend ai.Backend) ai.Backend {
	switch backend {
	case ai.BackendAnthropic, ai.BackendClaudeAgent, ai.BackendClaudeCLI, ai.BackendClaudeCmux:
		return ai.BackendAnthropic
	case ai.BackendOpenAI, ai.BackendCodexAgent, ai.BackendCodexCLI, ai.BackendCodexCmux:
		return ai.BackendOpenAI
	case ai.BackendGemini, ai.BackendGeminiCLI:
		return ai.BackendGemini
	case ai.BackendDeepSeek:
		return ai.BackendDeepSeek
	default:
		return ""
	}
}

func modelsForAdapterBackend(backend ai.Backend, models []ai.ModelDef) []ai.ModelDef {
	out := make([]ai.ModelDef, 0, len(models))
	positions := map[string]int{}
	for _, model := range models {
		if model.Backend == ai.BackendOpenAI && ai.IsIgnoredOpenAIModelID(model.ID) {
			continue
		}
		id := modelIDForAdapterBackend(backend, model.ID)
		if id == "" {
			continue
		}
		name := model.Name
		if name == "" {
			name = id
		}
		next := ai.ModelDef{ID: id, Name: name, Backend: backend, ReleaseDate: model.ReleaseDate}
		if idx, ok := positions[id]; ok {
			if modelDefNewer(next, out[idx]) {
				out[idx] = next
			}
			continue
		}
		positions[id] = len(out)
		out = append(out, next)
	}
	return out
}

func modelIDForAdapterBackend(backend ai.Backend, id string) string {
	return ai.NormalizeModelForBackend(backend, bareProviderModelID(id))
}

func modelDefNewer(left, right ai.ModelDef) bool {
	if left.ReleaseDate == "" {
		return false
	}
	if right.ReleaseDate == "" {
		return true
	}
	return left.ReleaseDate > right.ReleaseDate
}

func bareProviderModelID(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// setModels filters legacy entries and copies the sorted model list onto the
// adapter status as a count plus id list. The richer details are retained only
// for pretty output; JSON stays as the historical []string model list.
func setModels(st *AdapterStatus, models []ai.ModelDef) {
	models = ai.CurrentModelsByReleaseDate(models)
	st.ModelCount = len(models)
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	st.Models = ids
	st.ModelDetails = models
}
