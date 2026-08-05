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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
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
	// RecordAgent binds a key, an endpoint and a host key to an enrolled
	// agent — everything a dispatch to it needs.
	RecordAgent(AgentEnrollment) error
}

// ServerConfig configures one receive endpoint.
type ServerConfig struct {
	Listen    string
	Root      string // directory whose repos may be pushed to
	Role      ReceiverRole
	HostKey   gossh.Signer
	Directory AgentDirectory
	// Offer is what this endpoint hands back to a joining agent so the agent
	// can complete the reverse direction of trust. A mailbox that leaves it
	// empty enrolls agents it can dispatch to but that cannot relay back.
	Offer EnrollmentOffer
	// AgentRepoPath is the repository path an enrolled agent serves, used to
	// derive its dispatch URL when the agent advertises none.
	AgentRepoPath string
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

// handleEnroll completes both directions of the exchange: it records the
// agent's key and endpoint, and hands back the supervisor's dispatch key and
// mailbox path so the agent can authorize the reverse push.
func handleEnroll(s ssh.Session, cfg ServerConfig, fingerprint string, cmd []string) {
	if len(cmd) < 2 || strings.TrimSpace(cmd[1]) == "" {
		fmt.Fprintln(s.Stderr(), "captain: usage: captain-enroll <join-token> [request]")
		_ = s.Exit(1)
		return
	}
	req, err := decodeEnrollRequest(cmd)
	if err != nil {
		fmt.Fprintf(s.Stderr(), "captain: %v\n", err)
		_ = s.Exit(1)
		return
	}
	name, err := cfg.Directory.ConsumeJoinToken(strings.TrimSpace(cmd[1]))
	if err != nil {
		fmt.Fprintf(s.Stderr(), "captain: enrollment refused: %v\n", err)
		_ = s.Exit(1)
		return
	}
	enrollment := AgentEnrollment{
		Name:            name,
		Fingerprint:     fingerprint,
		URL:             agentDispatchURL(req, s.RemoteAddr(), cfg.AgentRepoPath),
		HostFingerprint: strings.TrimSpace(req.HostFingerprint),
	}
	if err := cfg.Directory.RecordAgent(enrollment); err != nil {
		fmt.Fprintf(s.Stderr(), "captain: enrollment failed: %v\n", err)
		_ = s.Exit(1)
		return
	}
	resp, err := json.Marshal(EnrollResponse{
		Agent:       name,
		DispatchKey: cfg.Offer.DispatchKey,
		MailboxPath: cfg.Offer.MailboxPath,
	})
	if err != nil {
		fmt.Fprintf(s.Stderr(), "captain: %v\n", err)
		_ = s.Exit(1)
		return
	}
	fmt.Fprintln(s, string(resp))
	_ = s.Exit(0)
}

func decodeEnrollRequest(cmd []string) (EnrollRequest, error) {
	var req EnrollRequest
	if len(cmd) < 3 || strings.TrimSpace(cmd[2]) == "" {
		return req, nil // an older client sent no details; derive what we can
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cmd[2]))
	if err != nil {
		return req, fmt.Errorf("unparseable enrollment request: %w", err)
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, fmt.Errorf("unparseable enrollment request: %w", err)
	}
	return req, nil
}

// agentDispatchURL resolves where the supervisor will dispatch to: the URL the
// agent advertised, or — on a flat network where the source address is the
// agent's real address — one derived from the connection plus its listen port.
func agentDispatchURL(req EnrollRequest, remote net.Addr, repoPath string) string {
	if url := strings.TrimSpace(req.AdvertiseURL); url != "" {
		return url
	}
	port := strings.TrimSpace(req.ListenPort)
	if port == "" || remote == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		return ""
	}
	if repoPath == "" {
		repoPath = "repo.git"
	}
	return "ssh://captain@" + net.JoinHostPort(host, port) + "/" + strings.TrimPrefix(repoPath, "/")
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
