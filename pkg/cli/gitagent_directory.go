package cli

import (
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
)

// gitAgentDirectory implements gitagent.AgentDirectory over the sandbox
// backend's options block in ~/.captain.yaml. Every read loads the file fresh
// so a revocation takes effect for the next connection (R8.5), and every
// mutation goes through the flocked captainconfig.Update (A3.4) so a token
// burn is atomic.
type gitAgentDirectory struct {
	backend string
}

func (d gitAgentDirectory) AgentByFingerprint(fingerprint string) (string, bool) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return "", false
	}
	backend, ok := cfg.Sandbox.Backends[d.backend]
	if !ok {
		return "", false
	}
	agents, _ := backend.Options["agents"].(map[string]any)
	for name, v := range agents {
		if entry, ok := v.(map[string]any); ok && entry["fingerprint"] == fingerprint {
			return name, true
		}
	}
	return "", false
}

func (d gitAgentDirectory) ConsumeJoinToken(token string) (string, error) {
	hash := gitagent.HashJoinToken(token)
	var agentName string
	var refusal error
	// The refusal travels outside the Update callback: an error returned from
	// the callback aborts the write, and burning an expired or malformed
	// token must persist.
	err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		refusal = fmt.Errorf("join token is unknown or already used")
		backend, ok := cfg.Sandbox.Backends[d.backend]
		if !ok {
			return nil
		}
		pending, _ := backend.Options["pending"].(map[string]any)
		entry, ok := pending[hash].(map[string]any)
		if !ok {
			return nil
		}
		// Burn before inspecting: a malformed entry must not stay redeemable.
		delete(pending, hash)
		if len(pending) == 0 {
			delete(backend.Options, "pending")
		}
		cfg.Sandbox.Backends[d.backend] = backend
		expires, _ := entry["expires"].(string)
		if t, err := time.Parse(time.RFC3339, expires); err != nil || time.Now().After(t) {
			refusal = fmt.Errorf("join token has expired; mint a new one with `captain sandbox git-agent add`")
			return nil
		}
		name, _ := entry["agent"].(string)
		if name == "" {
			refusal = fmt.Errorf("join token has no agent recorded")
			return nil
		}
		agentName = name
		refusal = nil
		return nil
	})
	if err != nil {
		return "", err
	}
	return agentName, refusal
}

func (d gitAgentDirectory) RecordAgentKey(name, fingerprint string) error {
	return captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend := ensureGitAgentBackend(cfg, d.backend)
		agents, _ := backend.Options["agents"].(map[string]any)
		if agents == nil {
			agents = map[string]any{}
		}
		agents[name] = map[string]any{
			"fingerprint": fingerprint,
			"addedAt":     time.Now().UTC().Format(time.RFC3339),
		}
		backend.Options["agents"] = agents
		cfg.Sandbox.Backends[d.backend] = backend
		return nil
	})
}

// ensureGitAgentBackend returns the named backend, creating a git-agent one
// (with an initialized Options map) when absent so `add` works on a fresh
// config.
func ensureGitAgentBackend(cfg *captainconfig.Config, name string) captainconfig.SandboxBackend {
	if cfg.Sandbox.Backends == nil {
		cfg.Sandbox.Backends = map[string]captainconfig.SandboxBackend{}
	}
	backend, ok := cfg.Sandbox.Backends[name]
	if !ok {
		backend = captainconfig.SandboxBackend{Kind: string(registry.SandboxGitAgent)}
	}
	if backend.Options == nil {
		backend.Options = map[string]any{}
	}
	cfg.Sandbox.Backends[name] = backend
	return backend
}
