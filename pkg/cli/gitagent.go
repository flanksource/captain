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
	"github.com/flanksource/clicky/api"
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

// The fixed layout every git-agent host uses, so enrollment and dispatch agree
// on where key material and repositories live without configuration.
const (
	hostKeyName       = "host_ed25519"       // this endpoint's SSH host key
	dispatchKeyName   = "supervisor_ed25519" // the supervisor's client key
	agentKeyName      = "agent_ed25519"      // the agent's client key
	servedReposDir    = "repos"              // served root, under the keys dir
	MailboxRepoName   = "mailbox.git"        // the supervisor's mailbox, under the root
	SidecarRepoName   = "repo.git"           // the agent's sidecar repo, under the root
	supervisorAgentID = "supervisor"         // the supervisor's identity on a sidecar
)

func gitAgentServedRoot() (string, error) {
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(keysDir, servedReposDir), nil
}

// gitAgentMailboxPath is where dispatch writes and the supervisor's endpoint
// serves. Keeping them the same path is what makes a relay reachable.
func gitAgentMailboxPath() (string, error) {
	root, err := gitAgentServedRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, MailboxRepoName), nil
}

// GitAgentHelp documents the group and the two-host setup, because the order
// of the steps is the part that is not guessable from the flags.
func GitAgentHelp() api.Textable {
	return clicky.Text("Remote git-agent sandboxes", "font-bold text-blue-400").NewLine().NewLine().
		AddText("A supervisor dispatches work to a coding agent on another machine; the agent", "text-gray-400").NewLine().
		AddText("runs only `git commit` and `git push`. Work is vetted at both ends before it", "text-gray-400").NewLine().
		AddText("is integrated.", "text-gray-400").NewLine().NewLine().
		AddText("Commands:", "font-bold text-blue-400").NewLine().
		AddText("  captain sandbox git-agent serve", "text-green-400").
		AddText("   — run a receive endpoint (this host)", "text-gray-500").NewLine().
		AddText("  captain sandbox git-agent add", "text-green-400").
		AddText("     — enroll an agent, print its join command", "text-gray-500").NewLine().
		AddText("  captain sandbox git-agent list", "text-green-400").
		AddText("    — enrolled agents and pending enrollments", "text-gray-500").NewLine().
		AddText("  captain sandbox git-agent revoke", "text-green-400").
		AddText("  — refuse an agent's key from now on", "text-gray-500").NewLine().NewLine().
		AddText("Setting up (supervisor first, then the agent host):", "font-bold text-blue-400").NewLine().
		AddText("  1. supervisor:  captain sandbox git-agent serve --role mailbox --repo /path/to/repo", "text-green-400").NewLine().
		AddText("  2. supervisor:  captain sandbox git-agent add worker-01 --endpoint ssh://<supervisor>:7422", "text-green-400").NewLine().
		AddText("  3. agent host:  run the printed join command (it enrolls, then serves)", "text-green-400").NewLine().
		AddText("  4. supervisor:  captain ai prompt ./task.prompt --sandbox git-agent", "text-green-400").NewLine().NewLine().
		AddText("Step 3 establishes trust in both directions: the supervisor learns the agent's", "text-gray-400").NewLine().
		AddText("endpoint and host key, and the agent authorizes the supervisor's dispatch key.", "text-gray-400").NewLine().NewLine().
		AddText("The agent:", "font-bold text-blue-400").NewLine().
		AddText("  By default the sidecar runs captain itself on the dispatched prompt, then", "text-gray-400").NewLine().
		AddText("  commits and pushes. Set sandbox.backends.<name>.agentCommand to run your own", "text-gray-400").NewLine().
		AddText("  agent instead, or ", "text-gray-400").
		AddText("agentCommand: none", "text-green-400").
		AddText(" to prepare the worktree and stop.", "text-gray-400").NewLine()
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
	DispatchKey     string    `json:"dispatchKey" pretty:"label=Dispatch key"`
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
	hostKeyPath := filepath.Join(keysDir, hostKeyName)
	dispatchKeyPath := filepath.Join(keysDir, dispatchKeyName)
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = gitAgentBackendEndpoint(opts.Backend)
	}
	if opts.DryRun {
		clicky.Printf("[dry-run] would ensure host key at %s\n", hostKeyPath)
		clicky.Printf("[dry-run] would ensure dispatch key at %s\n", dispatchKeyPath)
		clicky.Printf("[dry-run] would mint a single-use join token (TTL %s) for agent %q\n", gitagent.JoinTokenTTL, opts.Name)
		clicky.Printf("[dry-run] would record the pending enrollment under sandbox.backends.%s in %s\n", opts.Backend, configPathForDisplay())
		clicky.Printf("[dry-run] would print the join command for endpoint %s\n", endpoint)
		return nil, nil
	}
	_, hostFP, err := gitagent.EnsureKeyPair(hostKeyPath)
	if err != nil {
		return nil, err
	}
	// The dispatch key is what the agent must authorize for the supervisor's
	// push to be accepted, so it has to exist before the join is offered.
	_, dispatchFP, err := gitagent.EnsureKeyPair(dispatchKeyPath)
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
		backend.Options["dispatchKey"] = dispatchFP
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
		DispatchKey:     dispatchFP,
		JoinCommand:     join,
	}, nil
}

type GitAgentListOptions struct {
	Backend string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
}

type GitAgentListEntry struct {
	Name        string `json:"name" pretty:"label=Name"`
	Fingerprint string `json:"fingerprint,omitempty" pretty:"label=Fingerprint"`
	URL         string `json:"url,omitempty" pretty:"label=Endpoint"`
	AddedAt     string `json:"addedAt,omitempty" pretty:"label=Added"`
	Status      string `json:"status" pretty:"label=Status"`
}

// RunGitAgentList always returns a slice — an empty roster renders as [] in
// JSON rather than null, so a consumer can iterate it unconditionally.
func RunGitAgentList(opts GitAgentListOptions) (any, error) {
	entries := []GitAgentListEntry{}
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}
	backend, ok := cfg.Sandbox.Backends[opts.Backend]
	if !ok {
		return entries, nil
	}
	agents, _ := backend.Options["agents"].(map[string]any)
	for _, name := range sortedKeys(agents) {
		entry := GitAgentListEntry{Name: name, Status: "enrolled"}
		if m, ok := agents[name].(map[string]any); ok {
			entry.Fingerprint, _ = m["fingerprint"].(string)
			entry.URL, _ = m["url"].(string)
			entry.AddedAt, _ = m["addedAt"].(string)
		}
		entries = append(entries, entry)
	}
	pending, _ := backend.Options["pending"].(map[string]any)
	for _, hash := range sortedKeys(pending) {
		if m, ok := pending[hash].(map[string]any); ok {
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

// GitAgentRevokeResult reports the revocation, so --format json emits a
// single well-formed document rather than prose.
type GitAgentRevokeResult struct {
	Backend     string `json:"backend" pretty:"label=Backend"`
	Agent       string `json:"agent" pretty:"label=Agent"`
	Fingerprint string `json:"fingerprint,omitempty" pretty:"label=Fingerprint"`
	Revoked     bool   `json:"revoked" pretty:"label=Revoked"`
	DryRun      bool   `json:"dryRun,omitempty" pretty:"label=Dry Run"`
}

func RunGitAgentRevoke(opts GitAgentRevokeOptions) (any, error) {
	if opts.DryRun {
		clicky.Printf("[dry-run] would remove agent %q from sandbox.backends.%s.agents in %s\n",
			opts.Name, opts.Backend, configPathForDisplay())
		return GitAgentRevokeResult{Backend: opts.Backend, Agent: opts.Name, DryRun: true}, nil
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
	return GitAgentRevokeResult{
		Backend:     opts.Backend,
		Agent:       opts.Name,
		Fingerprint: fingerprint,
		Revoked:     true,
	}, nil
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
