package ai

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WhoamiOptions selects which adapters to probe and whether to list their
// models. The flag tags drive `captain whoami`; the struct lives here so the
// probe and its caching can be reused by non-CLI consumers (e.g. the aichat
// server's model menu) without importing pkg/cli.
type WhoamiOptions struct {
	Backend string `flag:"backend" help:"Show only this backend: anthropic|openai|gemini|deepseek|claude-cli|claude-agent|claude-cmux|codex-cli|codex-agent|codex-cmux|gemini-cli" short:"b"`
	Models  bool   `flag:"models" help:"List models from provider APIs or installed CLI catalogs" default:"true" short:"m"`
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

	ModelDetails []ModelDef `json:"modelDetails,omitempty"`
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

// AuthProbe abstracts the host environment (env vars, PATH, credential files)
// so resolveAdapter stays pure and testable. Fields are exported so callers in
// other packages (and their tests) can construct a hermetic probe.
type AuthProbe struct {
	Getenv      func(string) string
	LookPath    func(string) (string, error)
	FileExists  func(string) bool
	CodexModels func(context.Context, string) ([]ModelDef, error)
	Home        string
}

// OSAuthProbe wires AuthProbe to the real host environment.
func OSAuthProbe() AuthProbe {
	home, _ := os.UserHomeDir()
	return AuthProbe{
		Getenv:      os.Getenv,
		LookPath:    exec.LookPath,
		CodexModels: FetchCodexDebugModels,
		FileExists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		Home: home,
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
	st := AdapterStatus{Backend: string(backend), Type: backend.Kind()}

	for _, v := range AuthEnvVars(backend) {
		if val := p.Getenv(v); strings.TrimSpace(val) != "" {
			st.Authenticated = true
			st.AuthMethod = v + " (env)"
			st.AuthDetail = MaskKey(val)
			break
		}
	}

	if cli, ok := cliAdapters()[backend]; ok {
		if path, err := p.LookPath(cli.binary); err == nil {
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
		models = fetchAPIModels(backends, probe.Getenv)
		codexModels = fetchCodexModels(backends, probe)
	}

	adapters := make([]AdapterStatus, 0, len(backends))
	for _, b := range backends {
		st := resolveAdapter(b, probe)
		if opts.Models {
			applyModels(&st, b, models, codexModels, probe.Getenv)
		}
		adapters = append(adapters, st)
	}
	return adapters, nil
}
