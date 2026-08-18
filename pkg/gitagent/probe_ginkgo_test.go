package gitagent_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gossh "golang.org/x/crypto/ssh"

	"github.com/flanksource/captain/pkg/gitagent"
)

// sshServerPresenting starts a minimal SSH server on loopback with a freshly
// generated host key, and returns its endpoint and that key's fingerprint. It
// stands in for a git-agent receiver: the probe aborts at the key exchange, so
// nothing past the host key needs to be real.
func sshServerPresenting(keyPath string) (endpoint, fingerprint string) {
	GinkgoHelper()
	signer, fingerprint, err := gitagent.EnsureKeyPair(keyPath)
	Expect(err).NotTo(HaveOccurred())

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = listener.Close() })

	config := &gossh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	go func() {
		defer GinkgoRecover()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _, _, _ = gossh.NewServerConn(conn, config)
			}()
		}
	}()
	return "ssh://" + listener.Addr().String(), fingerprint
}

var _ = Describe("ProbeEndpoint", func() {
	var ctx context.Context

	BeforeEach(func() { ctx = context.Background() })

	It("reports the host key an endpoint presents", func() {
		endpoint, want := sshServerPresenting(filepath.Join(GinkgoT().TempDir(), "host_ed25519"))

		Expect(gitagent.ProbeEndpoint(ctx, endpoint)).To(Equal(want))
		Expect(gitagent.VerifyEndpointIdentity(ctx, endpoint, want)).To(Succeed())
	})

	// Enrolling against the wrong SSH server burns a single-use token before the
	// client discovers the mistake, so the mismatch has to be caught up front.
	It("refuses an endpoint presenting a different host key", func() {
		endpoint, _ := sshServerPresenting(filepath.Join(GinkgoT().TempDir(), "host_ed25519"))
		_, other := sshServerPresenting(filepath.Join(GinkgoT().TempDir(), "other_ed25519"))

		Expect(gitagent.VerifyEndpointIdentity(ctx, endpoint, other)).
			To(MatchError(ContainSubstring("another server holds that address")))
	})

	It("distinguishes nothing listening from something that is not SSH", func() {
		free, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		closed := "ssh://" + free.Addr().String()
		Expect(free.Close()).To(Succeed())

		_, err = gitagent.ProbeEndpoint(ctx, closed)
		Expect(err).To(MatchError(ContainSubstring("nothing is listening")))

		// A listener that accepts and then says nothing: a TCP dial would call
		// this healthy.
		silent, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(silent.Close)
		go func() {
			defer GinkgoRecover()
			conn, err := silent.Accept()
			if err == nil {
				DeferCleanup(conn.Close)
			}
		}()

		_, err = gitagent.ProbeEndpoint(ctx, "ssh://"+silent.Addr().String())
		Expect(err).To(MatchError(ContainSubstring("not a captain git-agent endpoint")))
	})
})

// tlsServerPresenting starts an HTTPS server holding a captain-generated
// certificate, and returns its endpoint plus the pin computed independently
// from the leaf — so the assertion has a reference value the probe did not produce.
func tlsServerPresenting(dir string) (endpoint, pin string) {
	GinkgoHelper()
	credential, err := gitagent.EnsureTLSCredential(dir, nil)
	Expect(err).NotTo(HaveOccurred())

	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	server.TLS = &tls.Config{Certificates: []tls.Certificate{credential.Certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	DeferCleanup(server.Close)

	spki, err := x509.MarshalPKIXPublicKey(credential.Leaf.PublicKey)
	Expect(err).NotTo(HaveOccurred())
	sum := sha256.Sum256(spki)
	return "https://" + server.Listener.Addr().String(), "sha256//" + base64.StdEncoding.EncodeToString(sum[:])
}

var _ = Describe("ProbeTLSPin", func() {
	var ctx context.Context

	BeforeEach(func() { ctx = context.Background() })

	It("reports the public-key pin an endpoint presents", func() {
		endpoint, want := tlsServerPresenting(GinkgoT().TempDir())

		Expect(gitagent.ProbeTLSPin(ctx, endpoint)).To(Equal(want))
		Expect(gitagent.VerifyEndpointIdentity(ctx, endpoint, want)).To(Succeed())
	})

	// The supervisor hands an agent a durable credential on this connection, so
	// reaching a different server than the one expected has to be caught before
	// the token is sent rather than after.
	It("refuses an endpoint presenting a different certificate", func() {
		endpoint, _ := tlsServerPresenting(GinkgoT().TempDir())
		_, other := tlsServerPresenting(GinkgoT().TempDir())

		Expect(gitagent.VerifyEndpointIdentity(ctx, endpoint, other)).
			To(MatchError(ContainSubstring("another server holds that address")))
	})

	It("distinguishes nothing listening from something that is not TLS", func() {
		free, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		closed := "https://" + free.Addr().String()
		Expect(free.Close()).To(Succeed())

		_, err = gitagent.ProbeTLSPin(ctx, closed)
		Expect(err).To(MatchError(ContainSubstring("nothing is listening")))

		// Plain HTTP on the port an https:// endpoint names: a TCP dial would
		// call this healthy, and `captain serve` without --tls is exactly it.
		plain := httptest.NewServer(http.NotFoundHandler())
		DeferCleanup(plain.Close)

		_, err = gitagent.ProbeTLSPin(ctx, "https://"+plain.Listener.Addr().String())
		Expect(err).To(MatchError(ContainSubstring("did not complete a TLS handshake")))
	})
})
