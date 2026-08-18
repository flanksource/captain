// Enrollment (§8): a captain token authorizes a key registration. The private
// key never leaves the agent host (R8.2).
//
// The token is durable rather than single-use (R8.2, amended). A burned token
// meant a long-lived sidecar — a container with --restart, a Deployment
// rescheduling — replayed a spent credential on every restart and crash-looped
// from the second start onward, which had to be worked around wherever
// enrollment could re-run. Bounding a credential by expiry and revocation
// instead of by one use removes that whole class of failure, and is what lets
// one token serve a scaled pool.
//
// The exchange is bidirectional because trust is: the supervisor dispatches TO
// the sidecar and the sidecar relays TO the mailbox, so each side must learn
// the other's endpoint and authorize the other's key. A one-way enrollment
// leaves a topology that looks configured but cannot complete a cycle.
package gitagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// EnrollRequest is what a joining agent tells the supervisor about itself.
type EnrollRequest struct {
	// Agent is the name a returning member persisted from an earlier
	// enrollment. It lets a pool member reclaim its slot across a restart
	// instead of consuming another; the supervisor honours it only when it is
	// already on file, so a client cannot invent an identity.
	Agent string `json:"agent,omitempty"`
	// AdvertiseURL is the sidecar endpoint the supervisor should dispatch to.
	// When empty the supervisor derives it from the connection's source
	// address and ListenPort, which is right on a flat network and wrong
	// behind NAT — hence the explicit override.
	AdvertiseURL string `json:"advertiseUrl,omitempty"`
	// ListenPort is the port the agent's own receive endpoint listens on.
	ListenPort string `json:"listenPort,omitempty"`
	// HostFingerprint is the agent endpoint's host key, for the supervisor to
	// pin when it dispatches. Set only for an ssh:// AdvertiseURL; an https
	// endpoint has no host key and carries DispatchToken instead.
	HostFingerprint string `json:"hostFingerprint"`
	// DispatchToken is a bearer credential this agent minted for the supervisor
	// to present on every dispatch push. It is the https sibling of
	// HostFingerprint: over ssh the supervisor is authenticated by the key
	// exchange, and over https there is no exchange to authenticate it with.
	//
	// A plain string, not text.SensitiveString: that type's MarshalJSON emits
	// "[REDACTED]" and it has no UnmarshalJSON, so the field would arrive as
	// that literal and the supervisor would record a credential this agent
	// rejects. Redaction belongs where the value is held — see
	// DispatchRequest.Token — not where it is transmitted.
	DispatchToken string `json:"dispatchToken,omitempty"`
}

// EnrollResponse is what the supervisor hands back so the agent can complete
// the reverse direction without manual configuration.
type EnrollResponse struct {
	Agent string `json:"agent"`
	// DispatchKey is the supervisor's client-key fingerprint. The agent
	// authorizes it locally so the supervisor's dispatch push is accepted.
	DispatchKey string `json:"dispatchKey"`
	// CACertificate is the supervisor's TLS certificate, PEM-encoded, handed
	// over the already-authenticated exchange so the agent's relays verify
	// against the endpoint it joined rather than the system trust store. Empty
	// for a supervisor that serves no HTTPS.
	CACertificate string `json:"caCertificate,omitempty"`
	// PinnedPublicKey is that certificate's sha256// pin, which survives a
	// re-issue under the same key.
	PinnedPublicKey string `json:"pinnedPubkey,omitempty"`
}

// EnrollmentOffer is the supervisor-side half of the exchange, supplied to
// the server by whatever runs it.
type EnrollmentOffer struct {
	DispatchKey     string
	CACertificate   string
	PinnedPublicKey string
}

// ResponseFor renders the offer for one admitted agent, so both transports
// hand back the same thing.
func (o EnrollmentOffer) ResponseFor(agent string) EnrollResponse {
	return EnrollResponse{
		Agent: agent, DispatchKey: o.DispatchKey,
		CACertificate: o.CACertificate, PinnedPublicKey: o.PinnedPublicKey,
	}
}

// AgentEnrollment is one recorded agent: its key, its endpoint, and the host
// key to pin when dispatching there.
type AgentEnrollment struct {
	Name        string
	Fingerprint string
	URL         string
	// HostFingerprint and DispatchToken are the two ways a supervisor proves
	// itself to this agent, and which one applies is decided by URL's scheme:
	// an ssh endpoint is pinned by host key, an https one authenticates with the
	// bearer token the agent minted. Exactly one is ever set.
	HostFingerprint string
	DispatchToken   string
}

// Enroll reaches the supervisor endpoint, presents the captain token along
// with this agent's endpoint details, and returns what the supervisor offered
// back. The supervisor's identity is verified against the fingerprint printed
// by `git-agent add` — never trusted on first use — which for ssh:// is its
// host key and for https:// is its certificate pin.
//
// The endpoint's scheme selects the channel. Both carry the same exchange, so
// everything downstream of the response is identical.
func Enroll(ctx context.Context, endpoint, token, hostFingerprint string, signer gossh.Signer, req EnrollRequest) (*EnrollResponse, error) {
	if EndpointScheme(endpoint) == "https" {
		return enrollHTTPS(ctx, endpoint, token, hostFingerprint, req)
	}
	hostFingerprint = strings.TrimSpace(hostFingerprint)
	if hostFingerprint == "" {
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

// MailboxURL joins the enrolled supervisor endpoint with the opaque route
// carried by a dispatch.
func MailboxURL(endpoint, mailboxPath string) (string, error) {
	if err := ValidateMailboxRoute(mailboxPath); err != nil {
		return "", err
	}
	if EndpointScheme(endpoint) == "https" {
		return HTTPSRepoURL(endpoint, mailboxPath)
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
		host := strings.TrimSuffix(strings.TrimPrefix(target, "["), "]")
		target = net.JoinHostPort(host, "22")
	}
	return target, user, nil
}
