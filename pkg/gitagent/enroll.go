// Enrollment (§8): a single-use, short-TTL join token authorizes exactly one
// key registration and is then burned. The private key never leaves the agent
// host (R8.2).
package gitagent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// JoinTokenTTL bounds how long a minted token stays redeemable.
const JoinTokenTTL = 15 * time.Minute

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

// Enroll dials the supervisor endpoint, presents the join token, and returns
// the server's confirmation line. The host key is verified against the
// fingerprint printed by `git-agent add` — never trusted on first use.
func Enroll(ctx context.Context, endpoint, token, hostFingerprint string, signer gossh.Signer) (string, error) {
	if strings.TrimSpace(hostFingerprint) == "" {
		return "", fmt.Errorf("enrollment requires the supervisor's host-key fingerprint (printed by `captain sandbox git-agent add`)")
	}
	addr, user, err := splitSSHEndpoint(endpoint)
	if err != nil {
		return "", err
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
		return "", err
	}
	c, chans, reqs, err := gossh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return "", err
	}
	client := gossh.NewClient(c, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	out, err := session.CombinedOutput(EnrollCommand + " " + token)
	if err != nil {
		return "", fmt.Errorf("enrollment refused: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
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
	if !strings.Contains(target, ":") {
		target += ":22"
	}
	return target, user, nil
}
