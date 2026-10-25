package gitagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// DialService opens a tunnel to one of a task's services on the endpoint
// target names, authenticating exactly as a push to it would: the enrolled key
// and pinned host key over ssh://, the captain token and pinned certificate
// over https://. target.URL is the task's mailbox URL.
func DialService(ctx context.Context, target TransportTarget, task, service string) (net.Conn, error) {
	if err := ValidateTaskID(task); err != nil {
		return nil, err
	}
	if !ValidServiceName(service) {
		return nil, fmt.Errorf("service name %q is not a valid service name", service)
	}
	endpoint, err := url.Parse(strings.TrimSpace(target.URL))
	if err != nil || endpoint.Host == "" {
		return nil, fmt.Errorf("tunnel endpoint %q is not a URL", target.URL)
	}
	switch EndpointScheme(target.URL) {
	case "ssh":
		return dialSSHService(endpoint, target, task, service)
	case "https":
		return dialHTTPSService(ctx, endpoint, target, task, service)
	default:
		return nil, fmt.Errorf("tunnel endpoint %q: captain tunnels over ssh:// and https:// only", target.URL)
	}
}

// TaskServiceTarget is how the sidecar reaches a task's services: the task's
// mailbox on the supervisor endpoint relay names, with relay's credentials.
func TaskServiceTarget(repo string, relay RelayTarget, task string) (TransportTarget, error) {
	state, ok, err := LoadTaskState(repo, task)
	if err != nil {
		return TransportTarget{}, err
	}
	if !ok {
		return TransportTarget{}, fmt.Errorf("task %s state is missing", task)
	}
	mailboxURL, err := MailboxURL(relay.URL, state.Mailbox)
	if err != nil {
		return TransportTarget{}, err
	}
	return relay.Transport(mailboxURL)
}

// Dialer binds DialService to one task's endpoint, for ServeForwarder.
func Dialer(target TransportTarget, task string) ServiceDialer {
	return func(ctx context.Context, service string) (net.Conn, error) {
		return DialService(ctx, target, task, service)
	}
}

func dialSSHService(endpoint *url.URL, target TransportTarget, task, service string) (net.Conn, error) {
	user := "captain"
	if endpoint.User != nil && endpoint.User.Username() != "" {
		user = endpoint.User.Username()
	}
	addr := endpoint.Host
	if endpoint.Port() == "" {
		addr = net.JoinHostPort(endpoint.Hostname(), "22")
	}
	repo := strings.TrimPrefix(endpoint.Path, "/")
	client, err := dialSSH(addr, user, target.KeyPath, target.HostFingerprint)
	if err != nil {
		return nil, fmt.Errorf("tunnel to %s: %w", service, err)
	}
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	conn := &sshServiceConn{client: client, session: session, stdin: stdin, stdout: stdout, service: service}
	session.Stderr = &conn.stderr
	if err := session.Start(strings.Join([]string{TunnelCommand, repo, task, service}, " ")); err != nil {
		_ = client.Close()
		return nil, err
	}
	return conn, nil
}

// sshServiceConn is a tunnel over one SSH exec session. A refused tunnel
// surfaces at the first read, as the server's reason rather than a bare EOF.
type sshServiceConn struct {
	client  *gossh.Client
	session *gossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	service string
	stderr  bytes.Buffer
	once    sync.Once
}

func (c *sshServiceConn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	if errors.Is(err, io.EOF) {
		if waitErr := c.session.Wait(); waitErr != nil {
			return n, fmt.Errorf("tunnel to %s: %s", c.service, strings.TrimSpace(c.stderr.String()))
		}
	}
	return n, err
}

func (c *sshServiceConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }
func (c *sshServiceConn) CloseWrite() error           { return c.stdin.Close() }

func (c *sshServiceConn) Close() error {
	var err error
	c.once.Do(func() { err = c.client.Close() })
	return err
}

func (c *sshServiceConn) LocalAddr() net.Addr              { return c.client.LocalAddr() }
func (c *sshServiceConn) RemoteAddr() net.Addr             { return c.client.RemoteAddr() }
func (c *sshServiceConn) SetDeadline(time.Time) error      { return nil }
func (c *sshServiceConn) SetReadDeadline(time.Time) error  { return nil }
func (c *sshServiceConn) SetWriteDeadline(time.Time) error { return nil }

func dialHTTPSService(ctx context.Context, endpoint *url.URL, target TransportTarget, task, service string) (net.Conn, error) {
	if target.Token.IsEmpty() {
		return nil, fmt.Errorf("tunnel to %s over https needs a captain token", service)
	}
	config, err := tunnelTLSConfig(endpoint.Hostname(), target)
	if err != nil {
		return nil, err
	}
	addr := endpoint.Host
	if endpoint.Port() == "" {
		addr = net.JoinHostPort(endpoint.Hostname(), "443")
	}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 30 * time.Second}, Config: config}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tunnel to %s: %w", service, err)
	}
	query := url.Values{"task": {task}, "service": {service}}
	path := strings.TrimSuffix(endpoint.Path, "/") + "/" + TunnelCommand + "?" + query.Encode()
	request := "GET " + path + " HTTP/1.1\r\nHost: " + endpoint.Host +
		"\r\nAuthorization: Bearer " + target.Token.Value() +
		"\r\nConnection: Upgrade\r\nUpgrade: " + TunnelCommand + "\r\n\r\n"
	if _, err := io.WriteString(raw, request); err != nil {
		_ = raw.Close()
		return nil, err
	}
	reader := bufio.NewReader(raw)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("tunnel to %s: %w", service, err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = raw.Close()
		return nil, fmt.Errorf("tunnel to %s refused (%s): %s", service, response.Status, strings.TrimSpace(string(body)))
	}
	return bufferedStream{Conn: raw, reader: reader}, nil
}

// tunnelTLSConfig trusts the endpoint the way its git transport does: its own
// certificate as the anchor when CAPath is set, and the public-key pin on top
// when one is recorded.
func tunnelTLSConfig(serverName string, target TransportTarget) (*tls.Config, error) {
	config := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	if target.CAPath != "" {
		pem, err := os.ReadFile(target.CAPath)
		if err != nil {
			return nil, fmt.Errorf("tunnel trust anchor: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tunnel trust anchor %s holds no certificate", target.CAPath)
		}
		config.RootCAs = roots
	}
	if pin := strings.TrimSpace(target.PinnedPublicKey); pin != "" {
		config.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("endpoint presented no certificate")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			got, err := publicKeyPin(leaf)
			if err != nil {
				return err
			}
			if got != pin {
				return fmt.Errorf("endpoint certificate pin %s does not match the pinned %s", got, pin)
			}
			return nil
		}
	}
	return config, nil
}
