// The captain sandbox git-agent command group (issue #39 §7): enrollment and
// the receive endpoint. `add` and `revoke` stay excluded from MCP exposure
// via the existing ^sandbox pattern (A7.3).
package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky"
)

// gitAgentKeysDir anchors key material beside the config file: with the
// default ~/.captain.yaml this is ~/.captain/sandbox, and tests that redirect
// the config path get an isolated keys dir for free.
func gitAgentKeysDir() (string, error) {
	path, err := captainconfig.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), ".captain", "sandbox"), nil
}

type GitAgentAddOptions struct {
	Name     string `args:"true" help:"Name for the agent being enrolled"`
	Backend  string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
	Endpoint string `flag:"endpoint" help:"ssh://host:port the new agent will join through (defaults to the backend's url)"`
	DryRun   bool   `flag:"dry-run" help:"Print every intended mutation without touching anything" short:"n"`
}

// GitAgentAddResult is the enrollment hand-off. The token is single-use and
// short-TTL; a private key is never printed (R8.2/A7.1).
type GitAgentAddResult struct {
	Backend         string    `json:"backend" pretty:"label=Backend"`
	Agent           string    `json:"agent" pretty:"label=Agent"`
	Expires         time.Time `json:"expires" pretty:"label=Token expires"`
	HostFingerprint string    `json:"hostFingerprint" pretty:"label=Host key"`
	JoinCommand     string    `json:"joinCommand" pretty:"label=Join command"`
}

func RunGitAgentAdd(opts GitAgentAddOptions) (any, error) {
	if err := gitagent.ValidateTaskID(opts.Name); err != nil {
		return nil, fmt.Errorf("agent name: %w", err)
	}
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return nil, err
	}
	hostKeyPath := filepath.Join(keysDir, "host_ed25519")
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = gitAgentBackendEndpoint(opts.Backend)
	}
	if opts.DryRun {
		clicky.Printf("[dry-run] would ensure host key at %s\n", hostKeyPath)
		clicky.Printf("[dry-run] would mint a single-use join token (TTL %s) for agent %q\n", gitagent.JoinTokenTTL, opts.Name)
		clicky.Printf("[dry-run] would record the pending enrollment under sandbox.backends.%s in %s\n", opts.Backend, configPathForDisplay())
		clicky.Printf("[dry-run] would print the join command for endpoint %s\n", endpoint)
		return nil, nil
	}
	_, hostFP, err := gitagent.EnsureKeyPair(hostKeyPath)
	if err != nil {
		return nil, err
	}
	token, hash, err := gitagent.MintJoinToken()
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(gitagent.JoinTokenTTL)
	err = captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend := ensureGitAgentBackend(cfg, opts.Backend)
		pending, _ := backend.Options["pending"].(map[string]any)
		if pending == nil {
			pending = map[string]any{}
		}
		pending[hash] = map[string]any{
			"agent":   opts.Name,
			"expires": expires.Format(time.RFC3339),
		}
		backend.Options["pending"] = pending
		cfg.Sandbox.Backends[opts.Backend] = backend
		return nil
	})
	if err != nil {
		return nil, err
	}
	join := fmt.Sprintf(
		"captain sandbox git-agent serve --join %s --supervisor %s --host-fingerprint %s",
		token, endpoint, hostFP)
	return GitAgentAddResult{
		Backend:         opts.Backend,
		Agent:           opts.Name,
		Expires:         expires,
		HostFingerprint: hostFP,
		JoinCommand:     join,
	}, nil
}

type GitAgentListOptions struct {
	Backend string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
}

type GitAgentListEntry struct {
	Name        string `json:"name" pretty:"label=Name"`
	Fingerprint string `json:"fingerprint,omitempty" pretty:"label=Fingerprint"`
	AddedAt     string `json:"addedAt,omitempty" pretty:"label=Added"`
	Status      string `json:"status" pretty:"label=Status"`
}

func RunGitAgentList(opts GitAgentListOptions) (any, error) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}
	backend, ok := cfg.Sandbox.Backends[opts.Backend]
	if !ok {
		return []GitAgentListEntry{}, nil
	}
	var entries []GitAgentListEntry
	agents, _ := backend.Options["agents"].(map[string]any)
	for name, v := range agents {
		entry := GitAgentListEntry{Name: name, Status: "enrolled"}
		if m, ok := v.(map[string]any); ok {
			entry.Fingerprint, _ = m["fingerprint"].(string)
			entry.AddedAt, _ = m["addedAt"].(string)
		}
		entries = append(entries, entry)
	}
	pending, _ := backend.Options["pending"].(map[string]any)
	for _, v := range pending {
		if m, ok := v.(map[string]any); ok {
			name, _ := m["agent"].(string)
			expires, _ := m["expires"].(string)
			entries = append(entries, GitAgentListEntry{Name: name, Status: "pending until " + expires})
		}
	}
	return entries, nil
}

type GitAgentRevokeOptions struct {
	Name    string `args:"true" help:"Enrolled agent name to revoke"`
	Backend string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
	DryRun  bool   `flag:"dry-run" help:"Print the intended mutation without touching anything" short:"n"`
}

func RunGitAgentRevoke(opts GitAgentRevokeOptions) (any, error) {
	if opts.DryRun {
		clicky.Printf("[dry-run] would remove agent %q from sandbox.backends.%s.agents in %s\n",
			opts.Name, opts.Backend, configPathForDisplay())
		return nil, nil
	}
	var fingerprint string
	err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, ok := cfg.Sandbox.Backends[opts.Backend]
		if !ok {
			return fmt.Errorf("backend %q has no enrolled agents", opts.Backend)
		}
		agents, _ := backend.Options["agents"].(map[string]any)
		entry, ok := agents[opts.Name].(map[string]any)
		if !ok {
			return fmt.Errorf("agent %q is not enrolled in backend %q", opts.Name, opts.Backend)
		}
		fingerprint, _ = entry["fingerprint"].(string)
		delete(agents, opts.Name)
		if len(agents) == 0 {
			delete(backend.Options, "agents")
		}
		cfg.Sandbox.Backends[opts.Backend] = backend
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Effective for connections established after now (R8.5): the server
	// consults the config per handshake.
	clicky.Printf("revoked %s (%s)\n", opts.Name, fingerprint)
	return nil, nil
}

func gitAgentBackendEndpoint(backend string) string {
	cfg, _, err := captainconfig.Load()
	if err == nil {
		if b, ok := cfg.Sandbox.Backends[backend]; ok {
			if url, _ := b.Options["url"].(string); url != "" {
				return url
			}
		}
	}
	return "ssh://<supervisor-host>:7422"
}

func configPathForDisplay() string {
	path, err := captainconfig.Path()
	if err != nil {
		return "~/.captain.yaml"
	}
	return path
}
