// Enrollment (§8): a single-use, short-TTL join token authorizes exactly one
// key registration and is then burned. The private key never leaves the agent
// host (R8.2).
//
// The exchange is bidirectional because trust is: the supervisor dispatches TO
// the sidecar and the sidecar relays TO the mailbox, so each side must learn
// the other's endpoint and authorize the other's key. A one-way enrollment
// leaves a topology that looks configured but cannot complete a cycle.
package gitagent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// JoinTokenTTL bounds how long a minted token stays redeemable.
const JoinTokenTTL = 15 * time.Minute

// EnrollRequest is what a joining agent tells the supervisor about itself.
type EnrollRequest struct {
	// AdvertiseURL is the sidecar endpoint the supervisor should dispatch to.
	// When empty the supervisor derives it from the connection's source
	// address and ListenPort, which is right on a flat network and wrong
	// behind NAT — hence the explicit override.
	AdvertiseURL string `json:"advertiseUrl,omitempty"`
	// ListenPort is the port the agent's own receive endpoint listens on.
	ListenPort string `json:"listenPort,omitempty"`
	// HostFingerprint is the agent endpoint's host key, for the supervisor to
	// pin when it dispatches.
	HostFingerprint string `json:"hostFingerprint"`
}

// EnrollResponse is what the supervisor hands back so the agent can complete
// the reverse direction without manual configuration.
type EnrollResponse struct {
	Agent string `json:"agent"`
	// DispatchKey is the supervisor's client-key fingerprint. The agent
	// authorizes it locally so the supervisor's dispatch push is accepted.
	DispatchKey string `json:"dispatchKey"`
	// MailboxPath is the mailbox repository's path under the supervisor's
	// served root. The agent joins it onto the endpoint it already dialed, so
	// the supervisor never has to know its own reachable hostname.
	MailboxPath string `json:"mailboxPath"`
}

// EnrollmentOffer is the supervisor-side half of the exchange, supplied to
// the server by whatever runs it.
type EnrollmentOffer struct {
	DispatchKey string
	MailboxPath string
}

// AgentEnrollment is one recorded agent: its key, its endpoint, and the host
// key to pin when dispatching there.
type AgentEnrollment struct {
	Name            string
	Fingerprint     string
	URL             string
	HostFingerprint string
}

// MintJoinToken returns a fresh token and its storage hash. Only the hash is
// persisted, so a leaked config file does not leak redeemable tokens.
func MintJoinToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashJoinToken(token), nil
}

// HashJoinToken maps a presented token onto its storage hash.
func HashJoinToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Enroll dials the supervisor endpoint, presents the join token along with
// this agent's endpoint details, and returns what the supervisor offered
// back. The host key is verified against the fingerprint printed by
// `git-agent add` — never trusted on first use.
func Enroll(ctx context.Context, endpoint, token, hostFingerprint string, signer gossh.Signer, req EnrollRequest) (*EnrollResponse, error) {
	if strings.TrimSpace(hostFingerprint) == "" {
		return nil, fmt.Errorf("enrollment requires the supervisor's host-key fingerprint (printed by `captain sandbox git-agent add`)")
	}
	addr, user, err := splitSSHEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	config := &gossh.ClientConfig{
		User: user,
		Auth: []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			if got := gossh.FingerprintSHA256(key); got != hostFingerprint {
				return fmt.Errorf("supervisor host key %s does not match the pinned %s", got, hostFingerprint)
			}
			return nil
		},
		Timeout: 30 * time.Second,
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := gossh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return nil, err
	}
	client := gossh.NewClient(c, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	command := EnrollCommand + " " + token + " " + base64.RawURLEncoding.EncodeToString(payload)
	// stderr is captured separately: the server explains a refusal there, and
	// folding it into stdout would corrupt the JSON response.
	var refusal strings.Builder
	session.Stderr = &refusal
	out, err := session.Output(command)
	if err != nil {
		return nil, fmt.Errorf("enrollment refused: %s", enrollFailureDetail(err, []byte(refusal.String())))
	}
	var resp EnrollResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("unparseable enrollment response %q: %w", strings.TrimSpace(string(out)), err)
	}
	if resp.Agent == "" || resp.DispatchKey == "" {
		return nil, fmt.Errorf("enrollment response is missing the agent name or the supervisor's dispatch key")
	}
	return &resp, nil
}

// enrollFailureDetail prefers the server's own explanation over the bare exit
// status, so a refusal reads as its reason.
func enrollFailureDetail(err error, out []byte) string {
	if detail := strings.TrimSpace(string(out)); detail != "" {
		return detail
	}
	var exitErr *gossh.ExitError
	if errors.As(err, &exitErr) && strings.TrimSpace(exitErr.Msg()) != "" {
		return strings.TrimSpace(exitErr.Msg())
	}
	return err.Error()
}

// MailboxURL joins the supervisor endpoint the agent dialed with the mailbox
// path the supervisor offered.
func MailboxURL(endpoint, mailboxPath string) (string, error) {
	if strings.TrimSpace(mailboxPath) == "" {
		return "", fmt.Errorf("the supervisor offered no mailbox path")
	}
	addr, user, err := splitSSHEndpoint(endpoint)
	if err != nil {
		return "", err
	}
	return "ssh://" + user + "@" + addr + "/" + strings.TrimPrefix(mailboxPath, "/"), nil
}

// splitSSHEndpoint accepts ssh://[user@]host[:port] or host[:port].
func splitSSHEndpoint(endpoint string) (addr, user string, err error) {
	user = "captain"
	target := endpoint
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "ssh" || u.Host == "" {
			return "", "", fmt.Errorf("endpoint %q must be ssh://[user@]host[:port]", endpoint)
		}
		if u.User != nil && u.User.Username() != "" {
			user = u.User.Username()
		}
		target = u.Host
	}
	if at := strings.LastIndex(target, "@"); at >= 0 {
		user, target = target[:at], target[at+1:]
	}
	if _, _, splitErr := net.SplitHostPort(target); splitErr != nil {
		target = net.JoinHostPort(target, "22")
	}
	return target, user, nil
}
