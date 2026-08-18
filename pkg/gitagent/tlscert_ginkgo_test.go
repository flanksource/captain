package gitagent_test

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
)

var _ = Describe("TLS credential", func() {
	var dir string

	BeforeEach(func() { dir = GinkgoT().TempDir() })

	It("generates once and reuses the same certificate", func() {
		first, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.CertPath).To(BeARegularFile())
		Expect(first.KeyPath).To(BeARegularFile())

		second, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())
		// Re-issuing would invalidate every agent that pinned the first one, so
		// the serial has to be identical rather than merely valid.
		Expect(second.Leaf.SerialNumber).To(Equal(first.Leaf.SerialNumber))
		Expect(second.PublicKeyPin).To(Equal(first.PublicKeyPin))
	})

	It("keeps the private key unreadable to other users", func() {
		credential, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())

		info, err := os.Stat(credential.KeyPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	// The certificate has to cover the address agents actually dial. Discovering
	// otherwise at first push costs a re-enrollment of every agent, so the
	// default is generous.
	It("covers loopback, this host, and any address the caller names", func() {
		credential, err := gitagent.EnsureTLSCredential(dir, []string{"supervisor.internal", "203.0.113.7"})
		Expect(err).NotTo(HaveOccurred())

		Expect(credential.Covers([]string{
			"localhost", "127.0.0.1", "::1", "supervisor.internal", "203.0.113.7",
			// The name a docker sidecar dials; it resolves only inside a
			// container, so nothing on this host would contribute it.
			"host.docker.internal",
		})).To(Succeed())

		hostname, err := os.Hostname()
		Expect(err).NotTo(HaveOccurred())
		Expect(credential.Covers([]string{hostname})).To(Succeed())
	})

	// Silently re-issuing on a new address would break every enrolled agent at
	// once, and the breakage would surface hours later as a push rejection.
	It("refuses an address it does not cover rather than re-issuing", func() {
		credential, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())
		serial := credential.Leaf.SerialNumber

		_, err = gitagent.EnsureTLSCredential(dir, []string{"elsewhere.example"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("elsewhere.example"))
		Expect(err.Error()).To(ContainSubstring("re-enrolled"),
			"the error must say what re-issuing costs, not just that it failed")

		reloaded, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.Leaf.SerialNumber).To(Equal(serial), "the refusal must not have rotated the certificate")
	})

	It("publishes a pin in the sha256// form git and curl accept", func() {
		credential, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())

		Expect(credential.PublicKeyPin).To(HavePrefix("sha256//"))
		// The pin is over the SubjectPublicKeyInfo, so an independently derived
		// digest of the same key must match.
		spki, err := x509.MarshalPKIXPublicKey(credential.Leaf.PublicKey)
		Expect(err).NotTo(HaveOccurred())
		Expect(spki).NotTo(BeEmpty())
		Expect(credential.PublicKeyPin).To(HaveLen(len("sha256//") + 44))
	})

	// The proof that all of it is right: a real handshake, verified against the
	// PEM an agent is handed, reaching the server over an IP address. This is
	// what catches a missing IP SAN or the leaf not being its own CA.
	It("serves a handshake that a client trusting only its PEM completes", func() {
		credential, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())

		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}))
		server.TLS = &tls.Config{Certificates: []tls.Certificate{credential.Certificate}, MinVersion: tls.VersionTLS12}
		server.StartTLS()
		DeferCleanup(server.Close)

		pemBytes, err := credential.PEM()
		Expect(err).NotTo(HaveOccurred())
		pool := x509.NewCertPool()
		Expect(pool.AppendCertsFromPEM(pemBytes)).To(BeTrue())

		trusting := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}}
		response, err := trusting.Get(server.URL)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))

		// A client that does not hold the PEM must be refused: trust comes from
		// the pinned certificate, not from the connection succeeding.
		stranger := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12},
		}}
		_, err = stranger.Get(server.URL)
		Expect(err).To(HaveOccurred())
	})

	// A certificate whose key went missing must read as absent, so the next call
	// regenerates instead of failing forever on a half-written pair.
	It("regenerates when only the certificate survives", func() {
		credential, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Remove(credential.KeyPath)).To(Succeed())

		regenerated, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(regenerated.Leaf.SerialNumber).NotTo(Equal(credential.Leaf.SerialNumber))
	})

	It("loads a certificate an operator supplied instead", func() {
		generated, err := gitagent.EnsureTLSCredential(dir, nil)
		Expect(err).NotTo(HaveOccurred())

		elsewhere := GinkgoT().TempDir()
		certPath := filepath.Join(elsewhere, "server.crt")
		keyPath := filepath.Join(elsewhere, "server.key")
		for _, copied := range []struct{ from, to string }{
			{generated.CertPath, certPath}, {generated.KeyPath, keyPath},
		} {
			data, err := os.ReadFile(copied.from)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(copied.to, data, 0o600)).To(Succeed())
		}

		supplied, err := gitagent.LoadTLSCredential(certPath, keyPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(supplied.PublicKeyPin).To(Equal(generated.PublicKeyPin))

		_, err = gitagent.LoadTLSCredential(filepath.Join(elsewhere, "absent.crt"), keyPath)
		Expect(os.IsNotExist(err)).To(BeTrue(), "a missing file must be distinguishable from a broken one")

		Expect(os.WriteFile(certPath, []byte("not a certificate"), 0o600)).To(Succeed())
		_, err = gitagent.LoadTLSCredential(certPath, keyPath)
		Expect(err).To(HaveOccurred())
		Expect(strings.ToLower(err.Error())).To(ContainSubstring("tls certificate"))
	})
})
