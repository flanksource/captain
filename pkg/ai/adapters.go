package ai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credentials"
)

// WhoamiOptions selects which adapters to probe and whether to list their
// models. The flag tags drive `captain whoami`; the struct lives here so the
// probe and its caching can be reused by non-CLI consumers (e.g. the aichat
// server's model menu) without importing pkg/cli.
type WhoamiOptions struct {
	Backend         string `flag:"backend" help:"Show only this backend: anthropic|openai|gemini|deepseek|claude-cli|claude-agent|claude-cmux|codex-cli|codex-agent|codex-cmux|gemini-cli" short:"b"`
	Models          bool   `flag:"models" help:"List models from provider APIs or installed CLI catalogs" default:"true" short:"m"`
	Limit           int    `flag:"limit" help:"Max sample model IDs to show per adapter in pretty output after per-prefix filtering (0 = all)" default:"0" short:"l"`
	IncludeDisabled bool   `flag:"disabled" help:"Include disabled models" default:"false"`
	NoCache         bool   `flag:"no-cache" help:"Bypass the persisted model and OpenRouter pricing caches and re-query both live" default:"false"`
}

// AdapterStatus is the resolved auth/availability of a single agent adapter
// (backend). Type is "api" for HTTP providers called with a key, "cli" for
// backends delegated to an installed coding-agent binary.
type AdapterStatus struct {
	Backend string `json:"backend"`
	Type    string `json:"type"`
	// Provider and Mode are the two axes Backend is a pair of. They are carried
	// on the wire so the whoami page can group and filter cards from registry
	// truth instead of re-deriving the mapping in TypeScript.
	Provider          string   `json:"provider"`
	Mode              string   `json:"mode"`
	Authenticated     bool     `json:"authenticated"`
	AuthMethod        string   `json:"authMethod,omitempty"`
	AuthDetail        string   `json:"authDetail,omitempty"`
	Binary            string   `json:"binary,omitempty"`
	BinaryMissing     string   `json:"binaryMissing,omitempty"`
	DependencyMissing string   `json:"dependencyMissing,omitempty"`
	Provisioner       string   `json:"provisioner,omitempty"`
	RuntimeError      string   `json:"runtimeError,omitempty"`
	ModelCount        int      `json:"modelCount"`
	Models            []string `json:"models,omitempty"`
	ModelError        string   `json:"modelError,omitempty"`

	ModelDetails []ModelDef `json:"modelDetails,omitempty"`

	// Disabled and DisabledReason are set by ApplyDisabled. The whoami page is the
	// one surface that annotates instead of dropping: hiding a disabled card would
	// leave no way to switch it back on. DisabledReason names the axis that did it
	// ("mode cmux", "provider openai", "backend claude-cmux") so the page can tell
	// a directly-toggled card apart from one switched off by its mode or provider.
	Disabled       bool   `json:"disabled,omitempty"`
	DisabledReason string `json:"disabledReason,omitempty"`
}

// Ready reports whether the adapter can actually run: authenticated, (for CLI
// backends) with its binary present in PATH, and not disabled by the user.
func (a AdapterStatus) Ready() bool {
	if !a.Authenticated || a.Disabled {
		return false
	}
	if a.Type == "cli" {
		return (a.Binary != "" || a.Provisioner != "") && a.DependencyMissing == "" && a.RuntimeError == ""
	}
	return true
}

// ApplyDisabled annotates a probe result with the user's opt-out set, marking
// backends and their models rather than removing them. It runs after
// CachedAdapters so the cache keeps raw probe data and a toggle takes effect on
// the next read instead of after the TTL expires.
func ApplyDisabled(adapters []AdapterStatus) []AdapterStatus {
	out := cloneAdapterStatuses(adapters)
	disabled := Disabled()
	if disabled.Empty() {
		return out
	}
	for i, a := range out {
		backend := Backend(a.Backend)
		a.Disabled = disabled.Backend(backend)
		a.DisabledReason = disabled.Reason(backend)
		for j, md := range a.ModelDetails {
			md.Disabled = a.Disabled || disabled.Model(backend, md.ID)
			md.SupportedEfforts = disabled.Efforts(md.SupportedEfforts)
			if disabled.Effort(md.DefaultEffort) {
				md.DefaultEffort = api.EffortNone
			}
			a.ModelDetails[j] = md
		}
		out[i] = a
	}
	return out
}

func cloneAdapterStatuses(adapters []AdapterStatus) []AdapterStatus {
	out := make([]AdapterStatus, len(adapters))
	for i, adapter := range adapters {
		adapter.Models = append([]string(nil), adapter.Models...)
		adapter.ModelDetails = cloneModelDefs(adapter.ModelDetails)
		out[i] = adapter
	}
	return out
}

func cloneModelDefs(models []ModelDef) []ModelDef {
	out := make([]ModelDef, len(models))
	for i, model := range models {
		model.InputMediaTypes = append([]string(nil), model.InputMediaTypes...)
		model.SupportedEfforts = append([]api.Effort(nil), model.SupportedEfforts...)
		out[i] = model
	}
	return out
}

// CredentialSnapshot is an immutable set of already-resolved API credentials.
// NewCredentialSnapshot clones its input, including an empty map, so callers can
// explicitly suppress process-global credential lookup. The zero value means no
// snapshot was supplied and lets ResolveModels resolve the relevant credentials
// once at operation start for backwards compatibility.
type CredentialSnapshot struct {
	apiKeys  map[Backend]api.ResolvedAPIKey
	supplied bool
}

func NewCredentialSnapshot(apiKeys map[Backend]api.ResolvedAPIKey) CredentialSnapshot {
	cloned := make(map[Backend]api.ResolvedAPIKey, len(apiKeys))
	for backend, resolved := range apiKeys {
		cloned[backend] = resolved
	}
	return CredentialSnapshot{apiKeys: cloned, supplied: true}
}

// APIKey returns the resolved credential for backend, or an empty value when
// that backend was not present in the snapshot.
func (s CredentialSnapshot) APIKey(backend Backend) api.ResolvedAPIKey {
	return s.apiKeys[backend]
}

func (s CredentialSnapshot) clone() CredentialSnapshot {
	if !s.supplied {
		return CredentialSnapshot{}
	}
	return NewCredentialSnapshot(s.apiKeys)
}

// AuthProbe abstracts the host environment (env vars, PATH, credential files)
// so resolveAdapter stays pure and testable. Fields are exported so callers in
// other packages (and their tests) can construct a hermetic probe.
type AuthProbe struct {
	Getenv             func(string) string
	LookPath           func(string) (string, error)
	FileExists         func(string) bool
	FileIdentity       func(string) string
	ExecutableIdentity func(string) string
	CodexModels        func(context.Context, string) ([]ModelDef, error)
	APICredentials     map[Backend]api.ResolvedAPIKey
	APIURLs            map[Backend]string
	RuntimeStatuses    map[Backend]RuntimeStatus
	ProbeError         error
	Home               string

	credentials      CredentialSnapshot
	stateFingerprint string
}

// OSAuthProbe wires AuthProbe to the real host environment.
func OSAuthProbe() AuthProbe {
	home, _ := os.UserHomeDir()
	probe := AuthProbe{
		Getenv:      os.Getenv,
		LookPath:    exec.LookPath,
		CodexModels: FetchCodexDebugModels,
		FileExists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		FileIdentity:       hostFileIdentity,
		ExecutableIdentity: hostExecutableIdentity,
		Home:               home,
	}
	probe.APICredentials = make(map[Backend]api.ResolvedAPIKey, len(apiBackends))
	for _, backend := range apiBackends {
		resolved, err := ResolveAPIKey(backend)
		if err != nil {
			probe.ProbeError = err
			break
		}
		probe.APICredentials[backend] = resolved
	}
	probe.APIURLs = make(map[Backend]string, len(apiBackends))
	for _, backend := range apiBackends {
		if apiURL := firstEnv(modelAPIURLEnvVars(backend), os.Getenv); apiURL != "" {
			probe.APIURLs[backend] = apiURL
		}
	}
	return probe
}

type frozenPathState struct {
	Path     string `json:"path,omitempty"`
	Identity string `json:"identity,omitempty"`
}

type frozenFileState struct {
	Exists   bool   `json:"exists"`
	Identity string `json:"identity,omitempty"`
}

type frozenCredentialState struct {
	Token  string `json:"token,omitempty"`
	Source string `json:"source,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type frozenProbeState struct {
	Home        string                            `json:"home"`
	Credentials map[Backend]frozenCredentialState `json:"credentials"`
	Environment map[string]string                 `json:"environment"`
	APIURLs     map[Backend]string                `json:"apiURLs"`
	Paths       map[string]frozenPathState        `json:"paths"`
	Files       map[string]frozenFileState        `json:"files"`
	Runtimes    map[Backend]RuntimeStatus         `json:"runtimes"`
}

// freezeAuthProbe eagerly captures every identity-bearing host observation used
// by adapter probing. Downstream auth reporting, cache identity, binary checks,
// and model discovery then consume values from one point-in-time snapshot rather
// than invoking live OS callbacks independently.
func freezeAuthProbe(probe AuthProbe) AuthProbe {
	if probe.stateFingerprint != "" {
		return probe
	}
	getenv := probe.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	environment := map[string]string{}
	for _, backend := range AllBackends() {
		if backend.Kind() == "cli" {
			for _, name := range AuthEnvVars(backend) {
				environment[name] = getenv(name)
			}
		}
		for _, name := range modelAPIURLEnvVars(backend) {
			environment[name] = getenv(name)
		}
	}
	probe.Getenv = func(name string) string { return environment[name] }

	if probe.credentials.supplied {
		probe.credentials = probe.credentials.clone()
	} else if probe.APICredentials != nil {
		probe.credentials = NewCredentialSnapshot(probe.APICredentials)
	} else {
		resolved := make(map[Backend]api.ResolvedAPIKey, len(apiBackends))
		for _, backend := range apiBackends {
			for _, name := range AuthEnvVars(backend) {
				if token := getenv(name); strings.TrimSpace(token) != "" {
					resolved[backend] = api.ResolvedAPIKey{Token: token, Source: credentials.SourceEnvironment, Detail: name}
					break
				}
			}
		}
		probe.credentials = NewCredentialSnapshot(resolved)
	}
	probe.APICredentials = nil

	apiURLsSupplied := probe.APIURLs != nil
	apiURLs := make(map[Backend]string, len(probe.APIURLs))
	for backend, apiURL := range probe.APIURLs {
		apiURLs[backend] = apiURL
	}
	if !apiURLsSupplied {
		for _, backend := range apiBackends {
			if apiURL := firstEnv(modelAPIURLEnvVars(backend), getenv); apiURL != "" {
				apiURLs[backend] = apiURL
			}
		}
	}
	probe.APIURLs = apiURLs

	lookPath := probe.LookPath
	if lookPath == nil {
		lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	}
	paths := map[string]frozenPathState{}
	executableIdentities := map[string]string{}
	for _, adapter := range cliAdapters() {
		if _, exists := paths[adapter.binary]; exists {
			continue
		}
		path, err := lookPath(adapter.binary)
		if err != nil || strings.TrimSpace(path) == "" {
			paths[adapter.binary] = frozenPathState{}
			continue
		}
		identity := ""
		if probe.ExecutableIdentity != nil {
			identity = probe.ExecutableIdentity(path)
		}
		executableIdentities[path] = identity
		paths[adapter.binary] = frozenPathState{Path: path, Identity: identity}
	}
	probe.LookPath = func(binary string) (string, error) {
		if state, ok := paths[binary]; ok && state.Path != "" {
			return state.Path, nil
		}
		return "", os.ErrNotExist
	}
	probe.ExecutableIdentity = func(path string) string { return executableIdentities[path] }

	fileExists := probe.FileExists
	if fileExists == nil {
		fileExists = func(string) bool { return false }
	}
	files := map[string]frozenFileState{}
	for _, adapter := range cliAdapters() {
		for _, login := range adapter.logins {
			path := filepath.Join(probe.Home, login.rel)
			if _, captured := files[path]; captured {
				continue
			}
			exists := fileExists(path)
			identity := ""
			if exists && probe.FileIdentity != nil {
				identity = probe.FileIdentity(path)
			}
			files[path] = frozenFileState{Exists: exists, Identity: identity}
		}
	}
	probe.FileExists = func(path string) bool { return files[path].Exists }
	probe.FileIdentity = func(path string) string { return files[path].Identity }

	runtimesSupplied := probe.RuntimeStatuses != nil
	runtimes := make(map[Backend]RuntimeStatus, len(probe.RuntimeStatuses))
	for backend, status := range probe.RuntimeStatuses {
		runtimes[backend] = status
	}
	if !runtimesSupplied {
		for backend := range cliAdapters() {
			if status, custom := probeRuntime(backend); custom {
				runtimes[backend] = status
			}
		}
	}
	probe.RuntimeStatuses = runtimes

	credentialState := make(map[Backend]frozenCredentialState, len(apiBackends))
	for _, backend := range apiBackends {
		resolved := probe.credentials.APIKey(backend)
		credentialState[backend] = frozenCredentialState{Token: resolved.Token, Source: resolved.Source, Detail: resolved.Detail}
	}
	state := frozenProbeState{
		Home:        probe.Home,
		Credentials: credentialState,
		Environment: environment,
		APIURLs:     apiURLs,
		Paths:       paths,
		Files:       files,
		Runtimes:    runtimes,
	}
	encoded, _ := json.Marshal(state)
	fingerprint := sha256.Sum256(encoded)
	probe.stateFingerprint = fmt.Sprintf("%x", fingerprint)
	return probe
}

func hostFileIdentity(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unreadable"
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", digest)
}

func hostExecutableIdentity(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "unreadable"
	}
	return fmt.Sprintf("size=%d|mode=%d|mtime=%d", info.Size(), info.Mode(), info.ModTime().UnixNano())
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

func cliAdapters() map[Backend]cliAdapter {
	claude := cliAdapter{
		binary: "claude",
		logins: []loginFile{
			{rel: filepath.Join(".claude", ".credentials.json"), label: "claude login"},
			{rel: ".claude.json", label: "claude login"},
		},
	}
	return map[Backend]cliAdapter{
		BackendClaudeAgent: claude,
		BackendClaudeCLI:   claude,
		BackendClaudeCmux:  claude,
		BackendCodexCLI: {
			binary: "codex",
			logins: []loginFile{{rel: filepath.Join(".codex", "auth.json"), label: "codex login"}},
		},
		BackendCodexAgent: {
			binary: "codex",
			logins: []loginFile{{rel: filepath.Join(".codex", "auth.json"), label: "codex login"}},
		},
		BackendCodexCmux: {
			binary: "codex",
			logins: []loginFile{{rel: filepath.Join(".codex", "auth.json"), label: "codex login"}},
		},
		BackendGeminiCLI: {
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
func resolveAdapter(backend Backend, p AuthProbe) AdapterStatus {
	p = freezeAuthProbe(p)
	return resolveAdapterFrozen(backend, p)
}

func resolveAdapterFrozen(backend Backend, p AuthProbe) AdapterStatus {
	st := AdapterStatus{
		Backend: string(backend), Type: backend.Kind(),
		Provider: string(backend.Provider()), Mode: string(backend.Mode()),
	}

	if backend.Kind() == "api" {
		if resolved := p.credentials.APIKey(backend); strings.TrimSpace(resolved.Token) != "" {
			st.Authenticated = true
			st.AuthDetail = MaskKey(resolved.Token)
			if resolved.Source == credentials.SourceVault {
				st.AuthMethod = "Captain vault"
			} else {
				st.AuthMethod = resolved.Detail + " (env)"
			}
		}
	} else {
		for _, v := range AuthEnvVars(backend) {
			if val := p.Getenv(v); strings.TrimSpace(val) != "" {
				st.Authenticated = true
				st.AuthMethod = v + " (env)"
				st.AuthDetail = MaskKey(val)
				break
			}
		}
	}

	if cli, ok := cliAdapters()[backend]; ok {
		if runtime, custom := p.RuntimeStatuses[backend]; custom {
			st.Binary = runtime.Binary
			st.BinaryMissing = runtime.BinaryMissing
			st.DependencyMissing = runtime.DependencyMissing
			st.Provisioner = runtime.Provisioner
			st.RuntimeError = runtime.Error
		} else if path, err := p.LookPath(cli.binary); err == nil {
			st.Binary = path
		} else {
			st.BinaryMissing = cli.binary
		}
		if !st.Authenticated {
			for _, lf := range cli.logins {
				full := filepath.Join(p.Home, lf.rel)
				if p.FileExists(full) {
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

// MaskKey renders a secret as the first and last four characters so the output
// is identifiable without ever exposing the full token.
func MaskKey(s string) string {
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

// ProbeAdapters resolves each backend's auth/availability and (when opts.Models)
// its model listing against the supplied environment probe. It is the shared,
// injectable core behind `captain whoami`, the prompt --schema builder, and the
// aichat server model menu, so passing a stub AuthProbe keeps callers hermetic
// (no live API calls when the probe reports no API keys).
func ProbeAdapters(opts WhoamiOptions, probe AuthProbe) ([]AdapterStatus, error) {
	if probe.ProbeError != nil {
		return nil, probe.ProbeError
	}
	probe = freezeAuthProbe(probe)
	backends := AllBackends()
	if opts.Backend != "" {
		b := Backend(opts.Backend)
		if !b.Valid() {
			return nil, fmt.Errorf("--backend must be one of: %s (got %q)", BackendList(), opts.Backend)
		}
		backends = []Backend{b}
	}

	var models map[Backend]modelFetch
	var codexModels modelFetch
	if opts.Models {
		models = fetchAPIModels(backends, probe.credentials, probe.APIURLs, opts.NoCache)
		codexModels = fetchCodexModels(backends, probe)
	}

	adapters := make([]AdapterStatus, 0, len(backends))
	for _, b := range backends {
		st := resolveAdapterFrozen(b, probe)
		if opts.Models {
			applyModels(&st, b, models, codexModels, probe)
		}
		adapters = append(adapters, st)
	}
	return adapters, nil
}
