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
	Mode            string `flag:"mode" help:"Show only this runtime mode: api|agent|cli|cmux"`
	Provider        string `flag:"provider" help:"Show only this provider: anthropic|openai|google|deepseek" short:"p"`
	Models          bool   `flag:"models" help:"List models from provider APIs or installed CLI catalogs" default:"true" short:"m"`
	Limit           int    `flag:"limit" help:"Max sample model IDs to show per adapter in pretty output after per-prefix filtering (0 = all)" default:"0" short:"l"`
	IncludeDisabled bool   `flag:"disabled" help:"Include disabled models" default:"false"`
	NoCache         bool   `flag:"no-cache" help:"Bypass the persisted model and OpenRouter pricing caches and re-query both live" default:"false"`
}

// AdapterStatus is the resolved auth/availability of one runtime — one
// (provider, mode) cell. Type is "api" for HTTP providers called with a key,
// "cli" for runtimes delegated to an installed coding-agent binary.
type AdapterStatus struct {
	Type string `json:"type"`
	// Provider and Mode are the runtime: which family owns the models and which
	// mechanism serves them. They are carried on the wire so the whoami page can
	// group and filter cards from registry truth instead of re-deriving it in
	// TypeScript.
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
	// ("mode cmux", "provider openai", "runtime anthropic cmux") so the page can
	// tell a directly-toggled card apart from one switched off by its mode or
	// provider.
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
		provider, _ := api.ProviderByName(a.Provider)
		mode := api.RuntimeMode(a.Mode)
		a.Disabled = disabled.Runtime(provider, mode)
		a.DisabledReason = disabled.Reason(provider, mode)
		for j, md := range a.ModelDetails {
			md.Disabled = a.Disabled || disabled.Model(provider, mode, md.ID)
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
	apiKeys  map[string]api.ResolvedAPIKey
	supplied bool
}

func NewCredentialSnapshot(apiKeys map[string]api.ResolvedAPIKey) CredentialSnapshot {
	cloned := make(map[string]api.ResolvedAPIKey, len(apiKeys))
	for provider, resolved := range apiKeys {
		cloned[provider] = resolved
	}
	return CredentialSnapshot{apiKeys: cloned, supplied: true}
}

// APIKey returns the resolved credential for a provider, or an empty value when
// that provider was not present in the snapshot.
func (s CredentialSnapshot) APIKey(p *ModelProvider) api.ResolvedAPIKey {
	if p == nil {
		return api.ResolvedAPIKey{}
	}
	return s.apiKeys[p.Name]
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
	Getenv       func(string) string
	LookPath     func(string) (string, error)
	FileExists   func(string) bool
	FileIdentity func(string) string
	// FileMetadataIdentity is a cheap change detector for FileIdentity. When it
	// is absent, cache validation falls back to FileIdentity.
	FileMetadataIdentity func(string) string
	ExecutableIdentity   func(string) string
	CodexModels          func(context.Context, string) ([]ModelDef, error)
	APICredentials       map[string]api.ResolvedAPIKey
	APIURLs              map[string]string
	RuntimeStatuses      map[Runtime]RuntimeStatus
	ProbeError           error
	Home                 string

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
		FileIdentity:         hostFileIdentity,
		FileMetadataIdentity: hostMetadataIdentity,
		ExecutableIdentity:   hostMetadataIdentity,
		Home:                 home,
	}
	probe.APICredentials = make(map[string]api.ResolvedAPIKey, len(apiProviders))
	for _, p := range apiProviders {
		resolved, err := ResolveAPIKey(p, ModeAPI)
		if err != nil {
			probe.ProbeError = err
			break
		}
		probe.APICredentials[p.Name] = resolved
	}
	probe.APIURLs = make(map[string]string, len(apiProviders))
	for _, p := range apiProviders {
		if apiURL := firstEnv(modelAPIURLEnvVars(p), os.Getenv); apiURL != "" {
			probe.APIURLs[p.Name] = apiURL
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
	Home        string                           `json:"home"`
	Credentials map[string]frozenCredentialState `json:"credentials"`
	Environment map[string]string                `json:"environment"`
	APIURLs     map[string]string                `json:"apiURLs"`
	Paths       map[string]frozenPathState       `json:"paths"`
	Files       map[string]frozenFileState       `json:"files"`
	// Runtimes is keyed provider → mode: a JSON object key is a string, and the
	// pair must not be flattened back into one token to fit.
	Runtimes map[string]map[RuntimeMode]RuntimeStatus `json:"runtimes"`
}

// frozenRuntimeStates nests a runtime-keyed map for JSON, which cannot encode a
// struct key. It nests rather than joining the pair into one string so the
// fingerprint never reintroduces a composite runtime id.
func frozenRuntimeStates(runtimes map[Runtime]RuntimeStatus) map[string]map[RuntimeMode]RuntimeStatus {
	out := make(map[string]map[RuntimeMode]RuntimeStatus, len(runtimes))
	for runtime, status := range runtimes {
		byMode, ok := out[runtime.Provider]
		if !ok {
			byMode = map[RuntimeMode]RuntimeStatus{}
			out[runtime.Provider] = byMode
		}
		byMode[runtime.Mode] = status
	}
	return out
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
	for _, runtime := range AllRuntimes() {
		p, _ := api.ProviderByName(runtime.Provider)
		if runtime.Mode.Kind() == "cli" {
			for _, name := range AuthEnvVars(p, runtime.Mode) {
				environment[name] = getenv(name)
			}
		}
		for _, name := range modelAPIURLEnvVars(p) {
			environment[name] = getenv(name)
		}
	}
	probe.Getenv = func(name string) string { return environment[name] }

	if probe.credentials.supplied {
		probe.credentials = probe.credentials.clone()
	} else if probe.APICredentials != nil {
		probe.credentials = NewCredentialSnapshot(probe.APICredentials)
	} else {
		resolved := make(map[string]api.ResolvedAPIKey, len(apiProviders))
		for _, p := range apiProviders {
			for _, name := range AuthEnvVars(p, ModeAPI) {
				if token := getenv(name); strings.TrimSpace(token) != "" {
					resolved[p.Name] = api.ResolvedAPIKey{Token: token, Source: credentials.SourceEnvironment, Detail: name}
					break
				}
			}
		}
		probe.credentials = NewCredentialSnapshot(resolved)
	}
	probe.APICredentials = nil

	apiURLsSupplied := probe.APIURLs != nil
	apiURLs := make(map[string]string, len(probe.APIURLs))
	for provider, apiURL := range probe.APIURLs {
		apiURLs[provider] = apiURL
	}
	if !apiURLsSupplied {
		for _, p := range apiProviders {
			if apiURL := firstEnv(modelAPIURLEnvVars(p), getenv); apiURL != "" {
				apiURLs[p.Name] = apiURL
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
	runtimes := make(map[Runtime]RuntimeStatus, len(probe.RuntimeStatuses))
	for runtime, status := range probe.RuntimeStatuses {
		runtimes[runtime] = status
	}
	if !runtimesSupplied {
		for _, runtime := range AllRuntimes() {
			p, _ := api.ProviderByName(runtime.Provider)
			if status, custom := probeRuntime(p, runtime.Mode); custom {
				runtimes[runtime] = status
			}
		}
	}
	probe.RuntimeStatuses = runtimes

	credentialState := make(map[string]frozenCredentialState, len(apiProviders))
	for _, p := range apiProviders {
		resolved := probe.credentials.APIKey(p)
		credentialState[p.Name] = frozenCredentialState{Token: resolved.Token, Source: resolved.Source, Detail: resolved.Detail}
	}
	state := frozenProbeState{
		Home:        probe.Home,
		Credentials: credentialState,
		Environment: environment,
		APIURLs:     apiURLs,
		Paths:       paths,
		Files:       files,
		Runtimes:    frozenRuntimeStates(runtimes),
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

func hostMetadataIdentity(path string) string {
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

// cliAdapter holds the local-transport metadata for one provider family: the
// binary that must be on PATH and the credential files that signal a completed
// login. It is keyed by provider, not by runtime: every local mode of a family
// drives the same binary and shares its login. The one per-mode exception is the
// executable itself, which ModeCapabilities.RequiredBinary carries (the Anthropic
// agent SDK runs under tsx, not claude).
type cliAdapter struct {
	binary string
	logins []loginFile
}

func cliAdapters() map[string]cliAdapter {
	return map[string]cliAdapter{
		Anthropic.Name: {
			binary: "claude",
			logins: []loginFile{
				{rel: filepath.Join(".claude", ".credentials.json"), label: "claude login"},
				{rel: ".claude.json", label: "claude login"},
			},
		},
		OpenAI.Name: {
			binary: "codex",
			logins: []loginFile{{rel: filepath.Join(".codex", "auth.json"), label: "codex login"}},
		},
		Google.Name: {
			binary: "gemini",
			logins: []loginFile{
				{rel: filepath.Join(".gemini", "oauth_creds.json"), label: "gemini login"},
				{rel: filepath.Join(".gemini", "google_accounts.json"), label: "gemini login"},
			},
		},
	}
}

// resolveAdapter determines a runtime's auth method and (for local transports)
// binary availability from the probed environment. An API-key env var always
// wins over a CLI login file because that is the path NewProvider/ListModels
// actually take.
func resolveAdapter(runtime Runtime, p AuthProbe) AdapterStatus {
	p = freezeAuthProbe(p)
	return resolveAdapterFrozen(runtime, p)
}

func resolveAdapterFrozen(runtime Runtime, p AuthProbe) AdapterStatus {
	provider, _ := api.ProviderByName(runtime.Provider)
	st := AdapterStatus{
		Type:     runtime.Mode.Kind(),
		Provider: runtime.Provider,
		Mode:     string(runtime.Mode),
	}

	if runtime.Mode.Kind() == "api" {
		if resolved := p.credentials.APIKey(provider); strings.TrimSpace(resolved.Token) != "" {
			st.Authenticated = true
			st.AuthDetail = MaskKey(resolved.Token)
			if resolved.Source == credentials.SourceVault {
				st.AuthMethod = "Captain vault"
			} else {
				st.AuthMethod = resolved.Detail + " (env)"
			}
		}
	} else {
		for _, v := range AuthEnvVars(provider, runtime.Mode) {
			if val := p.Getenv(v); strings.TrimSpace(val) != "" {
				st.Authenticated = true
				st.AuthMethod = v + " (env)"
				st.AuthDetail = MaskKey(val)
				break
			}
		}
	}

	if cli, ok := cliAdapters()[runtime.Provider]; ok && runtime.Mode.Kind() == "cli" {
		binary := cli.binary
		if caps, found := provider.Caps(runtime.Mode); found && caps.RequiredBinary != "" {
			binary = caps.RequiredBinary
		}
		if status, custom := p.RuntimeStatuses[runtime]; custom {
			st.Binary = status.Binary
			st.BinaryMissing = status.BinaryMissing
			st.DependencyMissing = status.DependencyMissing
			st.Provisioner = status.Provisioner
			st.RuntimeError = status.Error
		} else if path, err := p.LookPath(binary); err == nil {
			st.Binary = path
		} else {
			st.BinaryMissing = binary
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

// ProbeAdapters resolves each runtime's auth/availability and (when opts.Models)
// its model listing against the supplied environment probe. It is the shared,
// injectable core behind `captain whoami`, the prompt --schema builder, and the
// aichat server model menu, so passing a stub AuthProbe keeps callers hermetic
// (no live API calls when the probe reports no API keys).
func ProbeAdapters(opts WhoamiOptions, probe AuthProbe) ([]AdapterStatus, error) {
	if probe.ProbeError != nil {
		return nil, probe.ProbeError
	}
	probe = freezeAuthProbe(probe)
	runtimes, err := selectedRuntimes(opts)
	if err != nil {
		return nil, err
	}

	var models map[string]modelFetch
	var codexModels modelFetch
	if opts.Models {
		models = fetchAPIModels(runtimes, probe.credentials, probe.APIURLs, opts.NoCache)
		codexModels = fetchCodexModels(runtimes, probe)
	}

	adapters := make([]AdapterStatus, 0, len(runtimes))
	for _, runtime := range runtimes {
		st := resolveAdapterFrozen(runtime, probe)
		if opts.Models {
			applyModels(&st, runtime, models, codexModels, probe)
		}
		adapters = append(adapters, st)
	}
	return adapters, nil
}

// selectedRuntimes narrows the full runtime matrix by the two independent axes
// the caller may filter on. Neither axis is a runtime id: --provider names a
// family, --mode names a mechanism, and passing both selects one cell.
func selectedRuntimes(opts WhoamiOptions) ([]Runtime, error) {
	var mode RuntimeMode
	if raw := strings.TrimSpace(opts.Mode); raw != "" {
		parsed, ok := api.ParseRuntimeMode(raw)
		if !ok {
			return nil, fmt.Errorf("--mode must be one of: %s (got %q)", api.RuntimeModeList(), opts.Mode)
		}
		mode = parsed
	}
	provider := strings.TrimSpace(opts.Provider)
	if provider != "" {
		p, ok := api.ProviderByName(provider)
		if !ok {
			return nil, fmt.Errorf("--provider must be one of: %s (got %q)", api.ProviderList(), opts.Provider)
		}
		provider = p.Name
	}

	out := make([]Runtime, 0, len(AllRuntimes()))
	for _, runtime := range AllRuntimes() {
		if mode != "" && runtime.Mode != mode {
			continue
		}
		if provider != "" && runtime.Provider != provider {
			continue
		}
		out = append(out, runtime)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no runtime matches provider %q mode %q (available: %s)", opts.Provider, opts.Mode, api.RuntimeList())
	}
	return out, nil
}
