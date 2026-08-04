// The embedded SSH endpoint (§2/§8): it speaks git-receive-pack and the
// enrollment exchange, nothing else. upload-pack is never served — a shared
// upload-pack leaks every task namespace to every enrolled agent (R2.3/H11).
//
// The three gavel-serve defects issue #39 §9 records are corrected here: keys
// are authorized against an enrollment directory instead of accept-all, the
// vetting hooks live in pre-receive where rejection is possible, and the repo
// path is containment-checked rather than merely unquoted.
package gitagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Env vars the server injects into receive-pack so the hook shims know who is
// pushing and which admission tier they are.
const (
	EnvAgentName = "CAPTAIN_GITAGENT_AGENT"
	EnvRole      = "CAPTAIN_GITAGENT_ROLE"
)

// EnrollCommand is the single non-git exec verb the server accepts.
const EnrollCommand = "captain-enroll"

// AgentDirectory is the server's authorization source. Implementations read
// live state on every call so revocation takes effect for new connections
// (R8.5) and a join token can be burned atomically (R8.2).
type AgentDirectory interface {
	// AgentByFingerprint maps an SSH public-key SHA256 fingerprint to an
	// enrolled agent name.
	AgentByFingerprint(fingerprint string) (string, bool)
	// ConsumeJoinToken validates and burns a single-use join token, returning
	// the agent name it enrolls.
	ConsumeJoinToken(token string) (string, error)
	// RecordAgentKey binds a fingerprint to an enrolled agent.
	RecordAgentKey(name, fingerprint string) error
}

// ServerConfig configures one receive endpoint.
type ServerConfig struct {
	Listen    string
	Root      string // directory whose repos may be pushed to
	Role      ReceiverRole
	HostKey   gossh.Signer
	Directory AgentDirectory
}

// NewServer builds the SSH server. The caller owns ListenAndServe/Serve and
// Close.
func NewServer(cfg ServerConfig) (*ssh.Server, error) {
	if cfg.HostKey == nil || cfg.Directory == nil || cfg.Root == "" {
		return nil, fmt.Errorf("git-agent server needs a host key, an agent directory and a repo root")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	server := &ssh.Server{
		Addr: cfg.Listen,
		// The key handler only proves possession and records the fingerprint;
		// authorization is per-command below, because an unenrolled key must
		// still be able to present a join token (R8.2).
		PublicKeyHandler: func(ctx ssh.Context, key ssh.PublicKey) bool {
			ctx.SetValue("fingerprint", gossh.FingerprintSHA256(key))
			return true
		},
		Handler: func(s ssh.Session) {
			handleSession(s, root, cfg)
		},
	}
	server.AddHostKey(cfg.HostKey)
	return server, nil
}

func handleSession(s ssh.Session, root string, cfg ServerConfig) {
	fingerprint, _ := s.Context().Value("fingerprint").(string)
	cmd := s.Command()
	if len(cmd) == 0 {
		fmt.Fprintln(s.Stderr(), "captain: interactive sessions are not served")
		_ = s.Exit(1)
		return
	}
	switch cmd[0] {
	case EnrollCommand:
		handleEnroll(s, cfg, fingerprint, cmd)
	case "git-receive-pack", "git", "git-upload-pack", "git-upload-archive":
		handleGit(s, root, cfg, fingerprint, cmd)
	default:
		fmt.Fprintf(s.Stderr(), "captain: command %q is not served\n", cmd[0])
		_ = s.Exit(1)
	}
}

func handleEnroll(s ssh.Session, cfg ServerConfig, fingerprint string, cmd []string) {
	if len(cmd) != 2 || strings.TrimSpace(cmd[1]) == "" {
		fmt.Fprintln(s.Stderr(), "captain: usage: captain-enroll <join-token>")
		_ = s.Exit(1)
		return
	}
	name, err := cfg.Directory.ConsumeJoinToken(strings.TrimSpace(cmd[1]))
	if err != nil {
		fmt.Fprintf(s.Stderr(), "captain: enrollment refused: %v\n", err)
		_ = s.Exit(1)
		return
	}
	if err := cfg.Directory.RecordAgentKey(name, fingerprint); err != nil {
		fmt.Fprintf(s.Stderr(), "captain: enrollment failed: %v\n", err)
		_ = s.Exit(1)
		return
	}
	fmt.Fprintf(s, "enrolled %s %s\n", name, fingerprint)
	_ = s.Exit(0)
}

func handleGit(s ssh.Session, root string, cfg ServerConfig, fingerprint string, cmd []string) {
	repoArg, err := parseReceivePack(cmd)
	if err != nil {
		fmt.Fprintf(s.Stderr(), "captain: %v\n", err)
		_ = s.Exit(1)
		return
	}
	agent, ok := cfg.Directory.AgentByFingerprint(fingerprint)
	if !ok {
		fmt.Fprintf(s.Stderr(), "captain: key %s is not enrolled\n", fingerprint)
		_ = s.Exit(1)
		return
	}
	repo, err := ResolveRepoPath(root, repoArg)
	if err != nil {
		fmt.Fprintf(s.Stderr(), "captain: %v\n", err)
		_ = s.Exit(1)
		return
	}
	proc := exec.CommandContext(s.Context(), "git", "receive-pack", repo)
	proc.Env = envWith(os.Environ(), EnvAgentName+"="+agent, EnvRole+"="+string(cfg.Role))
	proc.Stdin = s
	proc.Stdout = s
	proc.Stderr = s.Stderr()
	if err := proc.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			_ = s.Exit(exit.ExitCode())
			return
		}
		fmt.Fprintf(s.Stderr(), "captain: receive-pack: %v\n", err)
		_ = s.Exit(1)
		return
	}
	_ = s.Exit(0)
}

// parseReceivePack accepts only the receive-pack command forms and returns
// the repo argument. upload-pack is refused by name (R2.3).
func parseReceivePack(cmd []string) (string, error) {
	switch cmd[0] {
	case "git-receive-pack":
		if len(cmd) != 2 {
			return "", fmt.Errorf("git-receive-pack takes exactly one repository argument")
		}
		return cmd[1], nil
	case "git":
		if len(cmd) != 3 || cmd[1] != "receive-pack" {
			return "", fmt.Errorf("only `git receive-pack <repo>` is served")
		}
		return cmd[2], nil
	default:
		return "", fmt.Errorf("%s is not served: this endpoint speaks git-receive-pack only (R2.3)", cmd[0])
	}
}

// ResolveRepoPath maps a client-supplied repo path onto a repo under root,
// confirming containment after resolving — stripping quotes and leading
// slashes is insufficient because `..` traversal escapes (R8.4/H13).
func ResolveRepoPath(root, arg string) (string, error) {
	cleaned := strings.TrimSpace(arg)
	cleaned = strings.Trim(cleaned, "'\"")
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" {
		return "", fmt.Errorf("empty repository path")
	}
	joined := filepath.Join(root, filepath.FromSlash(cleaned))
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("repository %q not found", cleaned)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("repository path %q escapes the served root (H13)", arg)
	}
	return resolved, nil
}
