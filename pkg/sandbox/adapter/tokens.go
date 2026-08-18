package adapter

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/flanksource/captain/pkg/sandbox"
	"gopkg.in/yaml.v3"
)

// Token acquisition for the local adapters.
//
// sandbox.TokenManager has existed — with per-provider acquirers, expiry and a
// refresh loop — but had no callers: a backend could declare `tokens:` in
// ~/.captain.yaml and nothing would ever acquire them. This is the seam that
// runs it, so the declaration finally means something for every provider, not
// just the two added for agent logins.

// sandboxTokens is one sandbox's acquired credentials: a private directory of
// credential files plus the environment that points tools at them.
type sandboxTokens struct {
	manager *sandbox.TokenManager
	credDir string
	env     map[string]string
	// providers is which providers were acquired, so a policy can tell whether
	// a redacted replacement exists before hiding the host's original.
	providers map[string]bool
}

// decodeTokensOption reads the backend's `tokens:` block. The options map comes
// from yaml.v3, so a round-trip through yaml decodes it into the same struct
// the configuration file declares.
func decodeTokensOption(options map[string]any) (*sandbox.TokensConfig, error) {
	raw, ok := options["tokens"]
	if !ok || raw == nil {
		return nil, nil
	}
	encoded, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-encode sandbox tokens option: %w", err)
	}
	var config sandbox.TokensConfig
	if err := yaml.Unmarshal(encoded, &config); err != nil {
		return nil, fmt.Errorf("parse sandbox tokens option: %w", err)
	}
	return &config, nil
}

// acquireSandboxTokens resolves the backend's declared credentials into a
// private directory. It returns nil when no tokens are declared.
//
// Acquisition failure is returned, never swallowed: a sandbox that starts
// without the credential it was configured to carry fails later, inside the
// agent, as an unexplained authentication error.
func acquireSandboxTokens(ctx context.Context, options map[string]any) (*sandboxTokens, error) {
	config, err := decodeTokensOption(options)
	if err != nil {
		return nil, err
	}
	if config == nil || len(sandbox.SelectedTokenProviders(config)) == 0 {
		return nil, nil
	}

	credDir, err := os.MkdirTemp("", "captain-sandbox-creds-")
	if err != nil {
		return nil, fmt.Errorf("create sandbox credential directory: %w", err)
	}
	if err := os.Chmod(credDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure sandbox credential directory: %w", err)
	}

	manager := sandbox.NewTokenManager(credDir)
	results, err := manager.Acquire(ctx, config)
	if err != nil {
		manager.Cleanup()
		return nil, err
	}

	tokens := &sandboxTokens{
		manager:   manager,
		credDir:   credDir,
		env:       map[string]string{},
		providers: map[string]bool{},
	}
	for _, result := range results {
		tokens.providers[result.Provider] = true
		for key, value := range result.EnvVars {
			tokens.env[key] = value
		}
	}
	return tokens, nil
}

// Env renders the acquired environment as KEY=VALUE, sorted so a wrapped
// command's environment is reproducible.
func (t *sandboxTokens) Env() []string {
	if t == nil {
		return nil
	}
	keys := make([]string, 0, len(t.env))
	for key := range t.env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+t.env[key])
	}
	return out
}

// Has reports whether a provider was acquired.
func (t *sandboxTokens) Has(provider string) bool {
	if t == nil {
		return false
	}
	return t.providers[provider]
}

// Dir is the private credential directory, or "" when nothing was acquired.
func (t *sandboxTokens) Dir() string {
	if t == nil {
		return ""
	}
	return t.credDir
}

// Cleanup removes the credential directory. Safe on a nil receiver so callers
// can defer it unconditionally.
func (t *sandboxTokens) Cleanup() {
	if t == nil || t.manager == nil {
		return
	}
	t.manager.Cleanup()
}
