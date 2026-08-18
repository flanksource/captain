// The captain sandbox git-agent command group (issue #39 §7): enrollment and
// the receive endpoint. `add` and `revoke` stay excluded from MCP exposure
// via the existing ^sandbox pattern (A7.3).
package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/text"
)

// The layout lives in pkg/gitagent so the ingest watcher, which cannot import
// this package, resolves the same directories from the same constants.
func gitAgentKeysDir() (string, error) { return gitagent.DefaultKeysDir() }

// The fixed layout every git-agent host uses, so enrollment and dispatch agree
// on where key material and repositories live without configuration.
const (
	hostKeyName       = "host_ed25519"       // this endpoint's SSH host key
	dispatchKeyName   = "supervisor_ed25519" // the supervisor's client key
	agentKeyName      = "agent_ed25519"      // the agent's client key
	SidecarRepoName   = "repo.git"           // the agent's sidecar repo, under the root
	supervisorAgentID = "supervisor"         // the supervisor's identity on a sidecar
	// supervisorCAName is the supervisor's TLS certificate as an enrolled agent
	// stores it, so a relay over https verifies against the endpoint it joined
	// rather than against whatever the system trust store happens to contain.
	supervisorCAName = "supervisor_ca.pem"
)

func gitAgentServedRoot() (string, error) { return gitagent.DefaultServedRoot() }

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
		AddText("  — refuse an agent's key from now on", "text-gray-500").NewLine().
		AddText("  captain sandbox git-agent deploy", "text-green-400").
		AddText("  — enroll and run a sidecar on docker or kubernetes", "text-gray-500").NewLine().
		AddText("  captain sandbox git-agent undeploy", "text-green-400").
		AddText("— tear that sidecar down and revoke it", "text-gray-500").NewLine().NewLine().
		AddText("Setting up over SSH (supervisor first, then the agent host):", "font-bold text-blue-400").NewLine().
		AddText("  1. supervisor:  captain sandbox git-agent serve --role mailbox --listen :7422", "text-green-400").NewLine().
		AddText("  2. supervisor:  captain sandbox git-agent add worker-01 --endpoint ssh://<supervisor>:7422", "text-green-400").NewLine().
		AddText("  3. agent host:  run the printed join command (it enrolls, then serves)", "text-green-400").NewLine().
		AddText("  4. supervisor:  captain ai prompt ./task.prompt --sandbox git-agent", "text-green-400").NewLine().NewLine().
		AddText("Or over HTTPS, with no separate mailbox process — `captain serve` hosts it:", "font-bold text-blue-400").NewLine().
		AddText("  1. supervisor:  captain serve --host 0.0.0.0 --tls --tls-host <supervisor>", "text-green-400").NewLine().
		AddText("  2. supervisor:  captain sandbox git-agent add worker-01 --endpoint https://<supervisor>:9020", "text-green-400").NewLine().
		AddText("  3. agent host:  run the printed join command", "text-green-400").NewLine().
		AddText("  Both flags in step 1 matter: without --host 0.0.0.0 no container can reach it,", "text-gray-400").NewLine().
		AddText("  and without --tls a token would cross the network in clear text. deploy refuses", "text-gray-400").NewLine().
		AddText("  either way and names the flag.", "text-gray-400").NewLine().NewLine().
		AddText("Tokens are durable: a restarting sidecar re-presents the same one instead of", "text-gray-400").NewLine().
		AddText("needing a new join. `--pool` mints one token that names many members, for a", "text-gray-400").NewLine().
		AddText("scaled deployment. See `captain token` to list or revoke them.", "text-gray-400").NewLine().NewLine().
		AddText("Or let deploy do steps 2 and 3 (it detects both addresses and refuses", "text-gray-400").NewLine().
		AddText("rather than enrolling an agent it cannot prove is reachable):", "text-gray-400").NewLine().
		AddText("  captain sandbox git-agent deploy worker-01 --target docker", "text-green-400").NewLine().
		AddText("  captain sandbox git-agent deploy worker-01 --target docker --dry-run", "text-green-400").NewLine().
		AddText("  It enrolls against whichever mailbox this host serves; --transport picks when", "text-gray-400").NewLine().
		AddText("  it serves both.", "text-gray-400").NewLine().NewLine().
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
	Name      string `args:"true" help:"Name for the agent being enrolled; a pool derives its member names from it"`
	Backend   string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
	Endpoint  string `flag:"endpoint" help:"ssh:// or https:// endpoint the new agent will join through (defaults to the backend's url)"`
	Pool      bool   `flag:"pool" help:"Mint one token that serves many agents, naming each member as it arrives"`
	MaxAgents int    `flag:"max-agents" help:"Cap a pool's members; 0 leaves it unbounded"`
	Expires   string `flag:"expires" help:"Token lifetime, e.g. 90d or 720h; empty never expires"`
	DryRun    bool   `flag:"dry-run" help:"Print every intended mutation without touching anything" short:"n"`
}

// GitAgentAddResult is the enrollment hand-off. A private key is never printed
// (R8.2/A7.1); the token is, once, because nothing stored can reproduce it.
type GitAgentAddResult struct {
	Backend string     `json:"backend" pretty:"label=Backend"`
	Agent   string     `json:"agent" pretty:"label=Agent"`
	TokenID string     `json:"tokenId,omitempty" pretty:"label=Token ID"`
	Pool    bool       `json:"pool,omitempty" pretty:"label=Pool"`
	Expires *time.Time `json:"expires,omitempty" pretty:"label=Token expires"`
	// HostFingerprint is what the joining agent pins: this host's SSH host key
	// for an ssh:// endpoint, the served certificate's public-key pin for an
	// https:// one. Both are passed as --host-fingerprint and both are compared
	// against what the supervisor actually presents.
	HostFingerprint string `json:"hostFingerprint" pretty:"label=Supervisor identity"`
	DispatchKey     string `json:"dispatchKey" pretty:"label=Dispatch key"`
	JoinCommand     string `json:"joinCommand" pretty:"label=Join command"`
	DryRun          bool   `json:"dryRun,omitempty" pretty:"label=Dry Run"`

	// Token is the raw credential, for in-process callers such as
	// `git-agent deploy` that must hand it to a workload rather than print it.
	// JoinCommand already embeds it for the human who has to type it, so this
	// adds no exposure — but it stays off both output surfaces (json:"-",
	// pretty:"-") so that a caller reading the struct never widens them.
	// Recovering the token by re-parsing JoinCommand would couple a caller to a
	// fmt.Sprintf.
	Token text.SensitiveString `json:"-" pretty:"-"`
}

func RunGitAgentAdd(ctx context.Context, opts GitAgentAddOptions) (any, error) {
	if err := gitagent.ValidateTaskID(opts.Name); err != nil {
		return nil, fmt.Errorf("agent name: %w", err)
	}
	expiresAt, err := parseTokenLifetime(opts.Expires)
	if err != nil {
		return nil, err
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
		clicky.Printf("[dry-run] would mint a durable captain token for agent %q\n", opts.Name)
		clicky.Printf("[dry-run] would record the dispatch key under sandbox.backends.%s in %s\n", opts.Backend, configPathForDisplay())
		clicky.Printf("[dry-run] would print the join command for endpoint %s\n", endpoint)
		return GitAgentAddResult{Backend: opts.Backend, Agent: opts.Name, Pool: opts.Pool, DryRun: true}, nil
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
	// Resolved before the mint: an endpoint whose identity cannot be named would
	// otherwise leave a live token behind for an agent that can never join.
	identity, err := joinIdentity(opts.Backend, endpoint, hostFP)
	if err != nil {
		return nil, err
	}
	db, err := captainServeDB(ctx)
	if err != nil {
		return nil, err
	}
	input := database.CreateAPITokenInput{
		Name: opts.Name, Scope: captaintoken.ScopeGit, Pool: opts.Pool,
		MaxAgents: opts.MaxAgents, ExpiresAt: expiresAt,
	}
	if !opts.Pool {
		input.Agent = opts.Name
	}
	token, secret, err := db.CreateAPIToken(ctx, input)
	if err != nil {
		return nil, err
	}
	err = captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, opts.Backend)
		if err != nil {
			return err
		}
		backend.Options["dispatchKey"] = dispatchFP
		cfg.Sandbox.Backends[opts.Backend] = backend
		return nil
	})
	if err != nil {
		return nil, err
	}
	join := fmt.Sprintf(
		"captain sandbox git-agent serve --token %s --supervisor %s --host-fingerprint %s",
		secret.Value(), endpoint, identity)
	return GitAgentAddResult{
		Backend:         opts.Backend,
		Agent:           opts.Name,
		TokenID:         token.TokenID,
		Pool:            token.Pool,
		Expires:         token.ExpiresAt,
		HostFingerprint: identity,
		DispatchKey:     dispatchFP,
		JoinCommand:     join,
		Token:           secret,
	}, nil
}

type GitAgentListOptions struct {
	Backend string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
}

type GitAgentListEntry struct {
	Name string `json:"name" pretty:"label=Name"`
	// Fingerprint is the agent's own client key, matched at the SSH handshake.
	Fingerprint string `json:"fingerprint,omitempty" pretty:"label=Fingerprint"`
	URL         string `json:"url,omitempty" pretty:"label=Endpoint"`
	// HostFingerprint is the agent's host key, pinned only for SSH dispatch.
	// HTTPS dispatch authenticates with a token whose path is never exposed.
	HostFingerprint string `json:"hostFingerprint,omitempty" pretty:"label=Host key"`
	AddedAt         string `json:"addedAt,omitempty" pretty:"label=Added"`
	Status          string `json:"status" pretty:"label=Status"`
	// Dispatchable is the transport-neutral readiness contract for roster
	// consumers. DispatchIssue is safe to expose: it names the missing class of
	// credential without revealing the HTTPS token path.
	Dispatchable  bool   `json:"dispatchable" pretty:"label=Dispatchable"`
	DispatchIssue string `json:"dispatchIssue,omitempty" pretty:"label=Dispatch issue"`
	// Deployment is set when captain placed this agent's sidecar itself. An
	// agent joined by hand has none, and cannot be torn down from here.
	Deployment *GitAgentDeployment `json:"deployment,omitempty" pretty:"label=Deployment"`
}

// RunGitAgentList always returns a slice — an empty roster renders as [] in
// JSON rather than null, so a consumer can iterate it unconditionally.
func RunGitAgentList(opts GitAgentListOptions) (any, error) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}
	backend, ok := cfg.Sandbox.Backends[opts.Backend]
	if !ok {
		return []GitAgentListEntry{}, nil
	}
	return gitAgentRoster(backend), nil
}

// gitAgentRoster decodes a git-agent backend's enrolled and pending agents out
// of its opaque options. The options map is untyped by design — each adapter
// decodes its own — so every read is a checked assert. Shared with the sandbox
// catalog so the CLI roster and the schema/HTTP roster cannot drift.
func gitAgentRoster(backend captainconfig.SandboxBackend) []GitAgentListEntry {
	entries := []GitAgentListEntry{}
	agents, _ := backend.Options["agents"].(map[string]any)
	deployments, _ := backend.Options["deployments"].(map[string]any)
	for _, name := range sortedKeys(agents) {
		entry := GitAgentListEntry{Name: name, Status: "enrolled"}
		m, _ := agents[name].(map[string]any)
		if m != nil {
			entry.Fingerprint, _ = m["fingerprint"].(string)
			entry.URL, _ = m["url"].(string)
			entry.HostFingerprint, _ = m["hostFingerprint"].(string)
			entry.AddedAt, _ = m["addedAt"].(string)
		}
		entry.Dispatchable, entry.DispatchIssue = gitAgentDispatchStatus(m)
		if record, ok := deployments[name].(map[string]any); ok {
			deployment := deploymentFromRecord(record)
			entry.Deployment = &deployment
		}
		entries = append(entries, entry)
	}
	// A workload captain placed that has not joined yet: the sidecar is still
	// starting, or it was deployed with --wait=false. Without this it is invisible
	// until it enrolls, so an operator who just deployed sees an empty roster and
	// no way to tear the workload down.
	for _, name := range sortedKeys(deployments) {
		if _, enrolled := agents[name]; enrolled {
			continue
		}
		record, ok := deployments[name].(map[string]any)
		if !ok {
			continue
		}
		deployment := deploymentFromRecord(record)
		entries = append(entries, GitAgentListEntry{
			Name: name, Status: "deployed — waiting to enroll", Deployment: &deployment,
		})
	}
	return entries
}

func gitAgentDispatchStatus(entry map[string]any) (bool, string) {
	endpoint, _ := entry["url"].(string)
	if strings.TrimSpace(endpoint) == "" {
		return false, "missing endpoint"
	}
	switch gitagent.EndpointScheme(endpoint) {
	case "ssh":
		hostFingerprint, _ := entry["hostFingerprint"].(string)
		if strings.TrimSpace(hostFingerprint) == "" {
			return false, "missing host key"
		}
	case "https":
		tokenPath, _ := entry["tokenPath"].(string)
		if strings.TrimSpace(tokenPath) == "" {
			return false, "missing dispatch token"
		}
		if _, err := gitagent.ReadTokenFile(tokenPath); err != nil {
			return false, "unreadable dispatch token"
		}
	default:
		return false, "unsupported endpoint transport"
	}
	return true, ""
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
		cfg, _, err := captainconfig.Load()
		if err != nil {
			return nil, err
		}
		if _, err := enrolledAgent(cfg, opts.Backend, opts.Name); err != nil {
			return nil, err
		}
		clicky.Printf("[dry-run] would remove agent %q from sandbox.backends.%s.agents in %s\n",
			opts.Name, opts.Backend, configPathForDisplay())
		return GitAgentRevokeResult{Backend: opts.Backend, Agent: opts.Name, DryRun: true}, nil
	}
	var fingerprint string
	err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		entry, err := enrolledAgent(*cfg, opts.Backend, opts.Name)
		if err != nil {
			return err
		}
		backend := cfg.Sandbox.Backends[opts.Backend]
		agents, _ := backend.Options["agents"].(map[string]any)
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
	// The roster entry is gone, so nothing reads the token any more — but a
	// credential left on disk is still a credential, and this is the only place
	// that knows the agent is finished.
	if err := removeDispatchTokenFile(opts.Name); err != nil {
		return nil, fmt.Errorf("agent %q was revoked but its dispatch token could not be removed: %w", opts.Name, err)
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

func enrolledAgent(cfg captainconfig.Config, backendName, agentName string) (map[string]any, error) {
	backend, ok := cfg.Sandbox.Backends[backendName]
	if !ok {
		return nil, fmt.Errorf("backend %q has no enrolled agents", backendName)
	}
	agents, _ := backend.Options["agents"].(map[string]any)
	entry, ok := agents[agentName].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("agent %q is not enrolled in backend %q", agentName, backendName)
	}
	return entry, nil
}

// joinIdentity is what the joining agent pins the supervisor by: this host's
// SSH host key over ssh, the served certificate's public-key pin over https.
//
// The pin comes from the mailbox record rather than from a certificate file on
// disk, because `captain serve` may be presenting one supplied with --tls-cert.
// Printing the wrong one produces a join that is refused at the last moment,
// with nothing in the message to say which half of the pair is wrong.
func joinIdentity(backendName, endpoint, hostFingerprint string) (string, error) {
	if gitagent.EndpointScheme(endpoint) != "https" {
		return hostFingerprint, nil
	}
	record, err := selectMailboxRecord(backendName, transportHTTPS)
	if err != nil {
		return "", err
	}
	if !record.Encrypted || record.Identity == "" {
		return "", fmt.Errorf(
			"endpoint %s is https, but `captain serve` on this host serves plain HTTP on %s, so there is no "+
				"certificate for the agent to pin; restart it with --tls --tls-host <address agents dial>",
			endpoint, record.Listen)
	}
	return record.Identity, nil
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
