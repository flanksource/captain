package ai

import (
	"context"
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
	disabled := Disabled()
	if disabled.Empty() {
		return adapters
	}
	out := make([]AdapterStatus, len(adapters))
	for i, a := range adapters {
		backend := Backend(a.Backend)
		a.Disabled = disabled.Backend(backend)
		a.DisabledReason = disabled.Reason(backend)
		details := make([]ModelDef, len(a.ModelDetails))
		for j, md := range a.ModelDetails {
			md.Disabled = a.Disabled || disabled.Model(backend, md.ID)
			md.SupportedEfforts = disabled.Efforts(md.SupportedEfforts)
			if disabled.Effort(md.DefaultEffort) {
				md.DefaultEffort = api.EffortNone
			}
			details[j] = md
		}
		a.ModelDetails = details
		out[i] = a
	}
	return out
}

// AuthProbe abstracts the host environment (env vars, PATH, credential files)
// so resolveAdapter stays pure and testable. Fields are exported so callers in
// other packages (and their tests) can construct a hermetic probe.
type AuthProbe struct {
	Getenv         func(string) string
	LookPath       func(string) (string, error)
	FileExists     func(string) bool
	CodexModels    func(context.Context, string) ([]ModelDef, error)
	APICredentials map[Backend]api.ResolvedAPIKey
	ProbeError     error
	Home           string
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
		Home: home,
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
	return probe
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
	st := AdapterStatus{
		Backend: string(backend), Type: backend.Kind(),
		Provider: string(backend.Provider()), Mode: string(backend.Mode()),
	}

	if backend.Kind() == "api" && p.APICredentials != nil {
		if resolved := p.APICredentials[backend]; resolved.Token != "" {
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
		if runtime, custom := probeRuntime(backend); custom {
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
		models = fetchAPIModels(backends, probe, opts.NoCache)
		codexModels = fetchCodexModels(backends, probe)
	}

	adapters := make([]AdapterStatus, 0, len(backends))
	for _, b := range backends {
		st := resolveAdapter(b, probe)
		if opts.Models {
			applyModels(&st, b, models, codexModels, probe)
		}
		adapters = append(adapters, st)
	}
	return adapters, nil
}
