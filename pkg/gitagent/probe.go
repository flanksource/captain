package gitagent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// probeTimeout bounds the whole probe. It is short because every use is a
// preflight against an endpoint expected to be up: a slow answer is itself the
// answer, and the caller has a mutation waiting behind it.
const probeTimeout = 5 * time.Second

// errProbeDone aborts the handshake once the host key is in hand. Returning it
// from the host-key callback stops the exchange before authentication is
// attempted, which is what makes the probe credential-free — in SSH the key
// exchange precedes auth, so the fingerprint is already known by this point.
var errProbeDone = errors.New("probe complete")

// ProbeEndpoint returns the SSH host-key fingerprint an endpoint presents.
//
// This is deliberately stronger than dialling the port. A git-agent endpoint
// and an unrelated sshd both accept a TCP connection on the address a config
// file claims, and enrolling against the wrong one burns a single-use token
// before the client learns its mistake. The fingerprint identifies which
// process is actually there, so the caller can compare it against the host key
// it expects and refuse rather than guess.
//
// It reports a distinguishable error for each way the address can be wrong:
// nothing listening, something listening that does not speak SSH, or an SSH
// server that is not the one expected.
func ProbeEndpoint(ctx context.Context, endpoint string) (string, error) {
	addr, user, err := splitSSHEndpoint(endpoint)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("nothing is listening on %s: %w", addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	var fingerprint string
	_, _, _, err = gossh.NewClientConn(conn, addr, &gossh.ClientConfig{
		User: user,
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			fingerprint = gossh.FingerprintSHA256(key)
			return errProbeDone
		},
	})
	if fingerprint == "" {
		return "", fmt.Errorf("the process on %s did not complete an SSH key exchange, so it is not a captain git-agent endpoint: %w", addr, err)
	}
	return fingerprint, nil
}

// ProbeTLSPin returns the public-key pin an HTTPS endpoint presents.
//
// The HTTPS counterpart of ProbeEndpoint, and the same argument applies: a
// captain supervisor and an unrelated web server both accept a connection on
// the address a config file claims. The pin identifies which process is there,
// because it can only be produced by whoever holds the matching private key —
// the same proof an SSH host key gives, so no request has to follow.
//
// Chain verification is replaced rather than skipped: the certificate is
// self-signed by design and has no chain, and the caller compares the pin.
func ProbeTLSPin(ctx context.Context, endpoint string) (string, error) {
	leaf, err := ProbeTLSCertificate(ctx, endpoint)
	if err != nil {
		return "", err
	}
	return publicKeyPin(leaf)
}

// VerifyEndpointCoversName proves the certificate an endpoint presents covers
// the name an agent will dial it by.
//
// Hostname verification happens at the agent, against a name this host may not
// even resolve — host.docker.internal exists only inside a container. Checking
// it here, against the certificate actually being served, is the difference
// between a refusal now and a sidecar that enrolls and then fails every relay
// with a TLS error naming neither the cause nor the fix.
func VerifyEndpointCoversName(ctx context.Context, endpoint, name string) error {
	leaf, err := ProbeTLSCertificate(ctx, endpoint)
	if err != nil {
		return err
	}
	if err := leaf.VerifyHostname(name); err != nil {
		return fmt.Errorf(
			"the certificate served at %s does not cover %q, which is the name the agent will dial (it covers %s); "+
				"restart the supervisor with --tls-host %s — if its certificate was generated, delete it and its key "+
				"first, and note that re-issuing means every enrolled agent must be re-enrolled",
			endpoint, name, certificateCoverage(leaf), name)
	}
	return nil
}

// ProbeTLSCertificate returns the leaf certificate an HTTPS endpoint presents.
func ProbeTLSCertificate(ctx context.Context, endpoint string) (*x509.Certificate, error) {
	addr, err := splitHTTPSEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("nothing is listening on %s: %w", addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client := tls.Client(conn, &tls.Config{
		// The pin is the whole check and the caller makes it, so hostname and
		// chain verification here would only reject the self-signed certificate
		// this exists to identify.
		InsecureSkipVerify: true, //nolint:gosec // the caller compares the pin or the names
		MinVersion:         tls.VersionTLS12,
	})
	if err := client.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf(
			"the process on %s did not complete a TLS handshake, so it is not a captain HTTPS endpoint "+
				"(a `captain serve` without --tls serves plain HTTP): %w", addr, err)
	}
	defer client.Close()
	certs := client.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, fmt.Errorf("the endpoint at %s presented no certificate", addr)
	}
	return certs[0], nil
}

// splitHTTPSEndpoint returns the host:port an https endpoint names, defaulting
// the port to 443 the way a browser would.
func splitHTTPSEndpoint(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("endpoint %q must be https://host[:port]", endpoint)
	}
	if parsed.Port() == "" {
		return net.JoinHostPort(parsed.Hostname(), "443"), nil
	}
	return parsed.Host, nil
}

// VerifyEndpointIdentity probes an endpoint and requires it to present an
// expected identity, naming the mismatch when it does not. The scheme selects
// what identity means: an SSH host key, or a TLS public-key pin.
func VerifyEndpointIdentity(ctx context.Context, endpoint, wantIdentity string) error {
	kind, probe := "SSH endpoint", ProbeEndpoint
	if EndpointScheme(endpoint) == "https" {
		kind, probe = "HTTPS endpoint", ProbeTLSPin
	}
	got, err := probe(ctx, endpoint)
	if err != nil {
		return err
	}
	if got != wantIdentity {
		return fmt.Errorf("the %s at %s presents %s, not the expected %s; another server holds that address",
			kind, endpoint, got, wantIdentity)
	}
	return nil
}
