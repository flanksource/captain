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
	Backend string `flag:"backend" help:"Show only this backend: anthropic|openai|gemini|claude-cli|claude-agent|codex-cli|gemini-cli" short:"b"`
	Models  bool   `flag:"models" help:"Probe each provider's models endpoint via a live API call" default:"true" short:"m"`
	Limit   int    `flag:"limit" help:"Max sample model IDs to show per adapter in pretty output (0 = all)" default:"10" short:"l"`
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
		ai.BackendCodexCLI: {
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

// parentBackend maps a CLI backend onto the API backend whose /v1/models
// endpoint and key it shares; API backends are their own parent.
func parentBackend(b ai.Backend) ai.Backend {
	switch b {
	case ai.BackendCodexCLI:
		return ai.BackendOpenAI
	case ai.BackendClaudeCLI, ai.BackendClaudeAgent:
		return ai.BackendAnthropic
	case ai.BackendGeminiCLI:
		return ai.BackendGemini
	default:
		return b
	}
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
	backends := ai.AllBackends()
	if opts.Backend != "" {
		b := ai.Backend(opts.Backend)
		if !b.Valid() {
			return nil, fmt.Errorf("--backend must be one of: %s (got %q)", ai.BackendList(), opts.Backend)
		}
		backends = []ai.Backend{b}
	}

	probe := osAuthProbe()

	var models map[ai.Backend]modelFetch
	if opts.Models {
		models = fetchParentModels(backends, probe.getenv)
	}

	result := WhoamiResult{sampleLimit: opts.Limit, showModels: opts.Models}
	for _, b := range backends {
		st := resolveAdapter(b, probe)
		if opts.Models {
			applyModels(&st, b, models, probe.getenv)
		}
		result.Adapters = append(result.Adapters, st)
	}
	return result, nil
}

type modelFetch struct {
	models []ai.ModelDef
	err    error
}

// fetchParentModels hits each distinct parent provider's models endpoint once,
// concurrently, so claude-cli and claude-agent don't both re-query Anthropic.
// A parent with no API key in the environment is skipped entirely (the listing
// endpoint requires the key even when the CLI is logged in via OAuth).
func fetchParentModels(backends []ai.Backend, getenv func(string) string) map[ai.Backend]modelFetch {
	parents := map[ai.Backend]bool{}
	for _, b := range backends {
		p := parentBackend(b)
		if firstEnv(ai.AuthEnvVars(p), getenv) != "" {
			parents[p] = true
		}
	}

	out := make(map[ai.Backend]modelFetch, len(parents))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for p := range parents {
		wg.Add(1)
		go func(parent ai.Backend) {
			defer wg.Done()
			m, err := ai.ListModels(context.Background(), parent)
			mu.Lock()
			out[parent] = modelFetch{models: m, err: err}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return out
}

// applyModels fills in the model listing (or the reason it is unavailable) for a
// single adapter from the pre-fetched per-parent results.
func applyModels(st *AdapterStatus, b ai.Backend, cache map[ai.Backend]modelFetch, getenv func(string) string) {
	envVars := ai.AuthEnvVars(b)
	if firstEnv(envVars, getenv) == "" {
		st.ModelError = "set " + strings.Join(envVars, " or ") + " to list models"
		return
	}

	fetch, ok := cache[parentBackend(b)]
	if !ok {
		return
	}
	if fetch.err != nil {
		st.ModelError = fetch.err.Error()
		return
	}

	st.ModelCount = len(fetch.models)
	ids := make([]string, 0, len(fetch.models))
	for _, m := range fetch.models {
		ids = append(ids, m.ID)
	}
	st.Models = ids
}
