package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky"
)

type GitAgentServeOptions struct {
	Backend         string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
	Listen          string `flag:"listen" help:"Address to serve git-receive-pack on" default:":7422"`
	Root            string `flag:"root" help:"Directory of receivable repos (default <keys-dir>/repos)"`
	Role            string `flag:"role" help:"Receiver role: sidecar (runs beside a coding agent) or mailbox (the supervisor's receiver)" default:"sidecar"`
	Advertise       string `flag:"advertise" help:"sidecar role: ssh://host:port the supervisor should dispatch to (default: the address the supervisor sees)"`
	Join            string `flag:"join" help:"Single-use join token printed by 'captain sandbox git-agent add'"`
	Supervisor      string `flag:"supervisor" help:"ssh://host:port of the supervisor to enroll with"`
	HostFingerprint string `flag:"host-fingerprint" help:"Pinned SHA256 fingerprint of the supervisor's host key"`
}

// RunGitAgentServe runs the receive endpoint on this host, optionally
// enrolling with a supervisor first. The agent keypair is generated locally
// on first start and its private half never leaves this machine (R8.2).
func RunGitAgentServe(ctx context.Context, opts GitAgentServeOptions) (any, error) {
	role := gitagent.ReceiverRole(opts.Role)
	if role != gitagent.RoleSidecar && role != gitagent.RoleMailbox {
		return nil, fmt.Errorf("--role must be %q or %q", gitagent.RoleSidecar, gitagent.RoleMailbox)
	}
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return nil, err
	}
	root := opts.Root
	if root == "" {
		if root, err = gitAgentServedRoot(); err != nil {
			return nil, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if opts.Join != "" {
		if err := joinSupervisor(ctx, opts, keysDir); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	// Reclaim worktrees orphaned by a crashed hook (R10.3).
	gitagent.PruneWorktrees(ctx, root)
	if err := ensureServedRepos(ctx, root, role, opts); err != nil {
		return nil, err
	}
	hostKey, hostFP, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, hostKeyName))
	if err != nil {
		return nil, err
	}
	offer, err := enrollmentOffer(role, keysDir)
	if err != nil {
		return nil, err
	}
	server, err := gitagent.NewServer(gitagent.ServerConfig{
		Listen:        opts.Listen,
		Root:          root,
		Role:          role,
		HostKey:       hostKey,
		Directory:     gitAgentDirectory{backend: opts.Backend},
		Offer:         offer,
		AgentRepoPath: SidecarRepoName,
	})
	if err != nil {
		return nil, err
	}
	clicky.Printf("captain git-agent %s serving %s on %s\n", role, root, opts.Listen)
	clicky.Printf("  host key: %s\n", hostFP)
	if role == gitagent.RoleMailbox {
		clicky.Printf("  enroll an agent with: captain sandbox git-agent add <name> --endpoint ssh://<this-host>:<port>\n")
	} else {
		monitor := newAgentTaskLogMonitor(filepath.Join(root, SidecarRepoName), os.Stdout, os.Stderr, log.Infof)
		if err := monitor.prime(); err != nil {
			log.Warnf("git-agent task log monitor: %v", err)
		}
		go monitor.run(ctx)
	}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.ListenAndServe(); err != nil && ctx.Err() == nil {
		return nil, err
	}
	return nil, nil
}

// enrollmentOffer is what this endpoint hands a joining agent. Only a mailbox
// has a dispatch key for the agent to authorize; task-specific mailbox routes
// arrive later in authenticated dispatch envelopes.
func enrollmentOffer(role gitagent.ReceiverRole, keysDir string) (gitagent.EnrollmentOffer, error) {
	if role != gitagent.RoleMailbox {
		return gitagent.EnrollmentOffer{}, nil
	}
	_, dispatchFP, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, dispatchKeyName))
	if err != nil {
		return gitagent.EnrollmentOffer{}, err
	}
	return gitagent.EnrollmentOffer{DispatchKey: dispatchFP}, nil
}

// joinSupervisor performs the enrollment exchange and records both directions
// of trust: the supervisor's dispatch key is authorized locally, and its base
// endpoint is retained for task-specific result relays.
func joinSupervisor(ctx context.Context, opts GitAgentServeOptions, keysDir string) error {
	if opts.Supervisor == "" {
		return fmt.Errorf("--join requires --supervisor ssh://host:port")
	}
	signer, fp, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, agentKeyName))
	if err != nil {
		return err
	}
	// The supervisor must be able to verify this endpoint's host key when it
	// dispatches, so the key has to exist before we advertise its fingerprint.
	_, hostFP, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, hostKeyName))
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(opts.Listen)
	if err != nil {
		return fmt.Errorf("--listen %q must be [host]:port: %w", opts.Listen, err)
	}
	// Verify the local half can be persisted before consuming the supervisor's
	// single-use token. The exchange cannot be transactional across hosts, but
	// this catches path, permission, and backend-kind failures up front.
	if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		_, err := ensureGitAgentBackend(cfg, opts.Backend)
		return err
	}); err != nil {
		return fmt.Errorf("prepare local enrollment config: %w", err)
	}
	resp, err := gitagent.Enroll(ctx, opts.Supervisor, opts.Join, opts.HostFingerprint, signer, gitagent.EnrollRequest{
		AdvertiseURL:    advertiseURL(opts.Advertise),
		ListenPort:      port,
		HostFingerprint: hostFP,
	})
	if err != nil {
		return err
	}
	err = captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, opts.Backend)
		if err != nil {
			return err
		}
		// The task supplies its repository-specific mailbox route; enrollment
		// records only the stable supervisor endpoint and host identity.
		backend.Options["supervisor"] = map[string]any{
			"url":             strings.TrimSuffix(opts.Supervisor, "/"),
			"hostFingerprint": strings.TrimSpace(opts.HostFingerprint),
		}
		// Authorize the supervisor's dispatch key so its push is accepted
		// here — the direction a one-way enrollment leaves broken.
		agents, _ := backend.Options["agents"].(map[string]any)
		if agents == nil {
			agents = map[string]any{}
		}
		agents[supervisorAgentID] = map[string]any{"fingerprint": resp.DispatchKey}
		backend.Options["agents"] = agents
		cfg.Sandbox.Backends[opts.Backend] = backend
		return nil
	})
	if err != nil {
		return fmt.Errorf("supervisor enrolled agent %q, but the local relay config could not be saved; mint a new join token and retry after fixing the config: %w", resp.Agent, err)
	}
	clicky.Printf("enrolled as %s\n", resp.Agent)
	clicky.Printf("  this agent's key:   %s\n", fp)
	clicky.Printf("  this endpoint's host key: %s\n", hostFP)
	clicky.Printf("  relays to:          %s/<task-mailbox>\n", strings.TrimSuffix(opts.Supervisor, "/"))
	clicky.Printf("  authorized supervisor key: %s\n", resp.DispatchKey)
	return nil
}

// advertiseURL normalizes an operator-supplied endpoint, appending the sidecar
// repository path when only a host:port was given.
func advertiseURL(raw string) string {
	advertise := strings.TrimSpace(raw)
	if advertise == "" {
		return ""
	}
	if !strings.Contains(advertise, "://") {
		advertise = "ssh://" + advertise
	}
	if trimmed := strings.TrimSuffix(advertise, "/"); !strings.Contains(strings.TrimPrefix(trimmed, "ssh://"), "/") {
		advertise = trimmed + "/" + SidecarRepoName
	}
	return advertise
}

// ensureServedRepos creates the role's repository and (re-)installs the hook
// shims on every repo under root, so an upgraded captain binary repoints the
// shims at itself.
func ensureServedRepos(ctx context.Context, root string, role gitagent.ReceiverRole, opts GitAgentServeOptions) error {
	switch role {
	case gitagent.RoleSidecar:
		if err := gitagent.InitSidecar(ctx, filepath.Join(root, SidecarRepoName)); err != nil {
			return err
		}
	case gitagent.RoleMailbox:
		if err := os.MkdirAll(filepath.Join(root, gitagent.MailboxesDir), 0o755); err != nil {
			return err
		}
		if err := recordMailboxRoot(opts.Backend, root); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Bake the config path into the shims: a hook inherits the pusher's
	// environment, and a co-located agent pushes from its own shell.
	configPath, err := captainconfig.Path()
	if err != nil {
		return err
	}
	var repos []string
	if role == gitagent.RoleSidecar {
		repos = append(repos, filepath.Join(root, SidecarRepoName))
	} else {
		entries, err := os.ReadDir(filepath.Join(root, gitagent.MailboxesDir))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				repos = append(repos, filepath.Join(root, gitagent.MailboxesDir, e.Name()))
			}
		}
	}
	for _, repo := range repos {
		if _, err := os.Stat(filepath.Join(repo, "HEAD")); err != nil {
			continue
		}
		if err := gitagent.InstallHookShims(repo, exe, configPath, role, opts.Backend); err != nil {
			return err
		}
	}
	return nil
}

// recordMailboxRoot lets dispatch processes create repository-specific
// mailboxes under the same root served by this long-running endpoint.
func recordMailboxRoot(backendName, root string) error {
	return captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, backendName)
		if err != nil {
			return err
		}
		backend.Options["mailboxRoot"] = root
		cfg.Sandbox.Backends[backendName] = backend
		return nil
	})
}
