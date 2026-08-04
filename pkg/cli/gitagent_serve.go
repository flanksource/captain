package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky"
)

type GitAgentServeOptions struct {
	Backend         string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
	Listen          string `flag:"listen" help:"Address to serve git-receive-pack on" default:":7422"`
	Root            string `flag:"root" help:"Directory of receivable repos (default <keys-dir>/repos)"`
	Role            string `flag:"role" help:"Receiver role: sidecar or mailbox" default:"sidecar"`
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
	if opts.Join != "" {
		if opts.Supervisor == "" {
			return nil, fmt.Errorf("--join requires --supervisor ssh://host:port")
		}
		signer, fp, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, "agent_ed25519"))
		if err != nil {
			return nil, err
		}
		confirmation, err := gitagent.Enroll(ctx, opts.Supervisor, opts.Join, opts.HostFingerprint, signer)
		if err != nil {
			return nil, err
		}
		clicky.Printf("%s\n", confirmation)
		clicky.Printf("this agent's key fingerprint: %s\n", fp)
	}
	root := opts.Root
	if root == "" {
		root = filepath.Join(keysDir, "repos")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	// Reclaim worktrees orphaned by a crashed hook (R10.3).
	gitagent.PruneWorktrees(ctx, root)
	hostKey, hostFP, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, "host_ed25519"))
	if err != nil {
		return nil, err
	}
	server, err := gitagent.NewServer(gitagent.ServerConfig{
		Listen:    opts.Listen,
		Root:      root,
		Role:      role,
		HostKey:   hostKey,
		Directory: gitAgentDirectory{backend: opts.Backend},
	})
	if err != nil {
		return nil, err
	}
	clicky.Printf("captain git-agent %s serving %s on %s (host key %s)\n", role, root, opts.Listen, hostFP)
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.ListenAndServe(); err != nil && ctx.Err() == nil {
		return nil, err
	}
	return nil, nil
}
