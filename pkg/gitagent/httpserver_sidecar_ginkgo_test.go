package gitagent_test

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

// sidecarFixture is the other end of the topology: an agent's own receive
// endpoint, over TLS, authenticating the SUPERVISOR by the bearer token the
// agent minted for it at enrollment. Enroll is nil because a sidecar enrolls
// nobody.
type sidecarFixture struct {
	root       string
	server     *httptest.Server
	credential *gitagent.TLSCredential
	secret     text.SensitiveString
	tokenPath  string
}

const sidecarPeer = "supervisor"

func newSidecarFixture() *sidecarFixture {
	GinkgoHelper()
	fixture := &sidecarFixture{root: GinkgoT().TempDir()}

	repo := filepath.Join(fixture.root, "repo.git")
	Expect(os.MkdirAll(repo, 0o755)).To(Succeed())
	runGitIn(repo, "init", "--bare", "-q", ".")
	runGitIn(repo, "symbolic-ref", "HEAD", "refs/heads/placeholder")

	keysDir := GinkgoT().TempDir()
	var err error
	fixture.secret, err = gitagent.MintDispatchCredential(
		filepath.Join(keysDir, gitagent.DispatchCredentialName))
	Expect(err).NotTo(HaveOccurred())
	credential, err := gitagent.LoadDispatchCredential(
		filepath.Join(keysDir, gitagent.DispatchCredentialName))
	Expect(err).NotTo(HaveOccurred())
	verifier := credential.Verifier(sidecarPeer)

	handler, err := gitagent.NewHTTPHandler(gitagent.HTTPServerConfig{
		Root: fixture.root,
		Role: gitagent.RoleSidecar,
		Identify: func(r *http.Request) (string, error) {
			presented, ok := captaintoken.BearerFromHeader(r.Header.Get("Authorization"))
			if !ok {
				return "", fmt.Errorf("no bearer token")
			}
			record, err := verifier.VerifyScope(r.Context(), presented, captaintoken.ScopeGit)
			if err != nil {
				return "", err
			}
			return record.Agent, nil
		},
		// A sidecar receives pushes and enrolls nobody.
		Enroll: nil,
	})
	Expect(err).NotTo(HaveOccurred())

	fixture.credential, err = gitagent.EnsureTLSCredential(keysDir, nil)
	Expect(err).NotTo(HaveOccurred())
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{fixture.credential.Certificate},
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	DeferCleanup(server.Close)
	fixture.server = server

	fixture.tokenPath = filepath.Join(keysDir, "dispatch.token")
	Expect(gitagent.WriteTokenFile(fixture.tokenPath, fixture.secret)).To(Succeed())
	return fixture
}

func (f *sidecarFixture) client() *http.Client {
	GinkgoHelper()
	pem, err := f.credential.PEM()
	Expect(err).NotTo(HaveOccurred())
	pool := x509.NewCertPool()
	Expect(pool.AppendCertsFromPEM(pem)).To(BeTrue())
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
}

// get issues a request with whatever Authorization header a test wants, so the
// three auth outcomes can be compared against each other.
func (f *sidecarFixture) get(path, authorization string) *http.Response {
	GinkgoHelper()
	req, err := http.NewRequest(http.MethodGet, f.server.URL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	response, err := f.client().Do(req)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(response.Body.Close)
	return response
}

var _ = Describe("sidecar receive endpoint over https", func() {
	const refs = gitagent.GitHTTPPrefix + "repo.git/info/refs?service=git-receive-pack"

	It("admits the supervisor's dispatch token", func() {
		fixture := newSidecarFixture()

		response := fixture.get(refs, "Bearer "+fixture.secret.Value())
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		Expect(readAll(response.Body)).To(ContainSubstring("side-band-64k"))
	})

	// The endpoint is reachable from outside the cluster, so a missing and a
	// wrong credential must be indistinguishable — anything else would reveal
	// which half of the token an attacker got right.
	It("refuses a missing and a wrong token identically", func() {
		fixture := newSidecarFixture()

		absent := fixture.get(refs, "")
		wrong := fixture.get(refs, "Bearer cptn_aaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		Expect(absent.StatusCode).To(Equal(http.StatusForbidden))
		Expect(wrong.StatusCode).To(Equal(absent.StatusCode))
		Expect(readAll(wrong.Body)).To(Equal(readAll(absent.Body)))
	})

	// The credential is git-scoped and issued by this agent alone, so another
	// agent's token must not open this one.
	It("refuses another agent's dispatch token", func() {
		fixture := newSidecarFixture()
		other, err := gitagent.MintDispatchCredential(
			filepath.Join(GinkgoT().TempDir(), gitagent.DispatchCredentialName))
		Expect(err).NotTo(HaveOccurred())

		Expect(fixture.get(refs, "Bearer "+other.Value()).StatusCode).To(Equal(http.StatusForbidden))
	})

	// Enroll is nil, so the endpoint is not served at all. A sidecar that could
	// be made to enrol something would be a second way into the roster.
	It("serves no enrollment endpoint", func() {
		fixture := newSidecarFixture()

		req, err := http.NewRequest(http.MethodPost, fixture.server.URL+gitagent.GitHTTPPrefix+"enroll", nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Authorization", "Bearer "+fixture.secret.Value())
		response, err := fixture.client().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusNotFound))
	})

	It("refuses upload-pack even for the supervisor (R2.3)", func() {
		fixture := newSidecarFixture()

		response := fixture.get(
			gitagent.GitHTTPPrefix+"repo.git/git-upload-pack", "Bearer "+fixture.secret.Value())
		Expect(response.StatusCode).To(Equal(http.StatusForbidden))
	})

	// The whole point: a dispatch is a git push, driven by the same TransportEnv
	// the supervisor uses, and the hooks must see the peer as the supervisor so
	// the ref-namespace rules have an identity to enforce against.
	It("accepts a real dispatch push and attributes it to the supervisor", func() {
		fixture := newSidecarFixture()
		repo := filepath.Join(fixture.root, "repo.git")
		hookPath := filepath.Join(repo, "hooks", "pre-receive")
		Expect(os.MkdirAll(filepath.Dir(hookPath), 0o755)).To(Succeed())
		Expect(os.WriteFile(hookPath, []byte(
			"#!/bin/sh\ncat >/dev/null\necho \"pushed by ${"+gitagent.EnvAgentName+"} as ${"+gitagent.EnvRole+"}\" >&2\nexit 1\n",
		), 0o755)).To(Succeed())

		work := GinkgoT().TempDir()
		runGitIn(work, "init", "-q", "-b", "main", ".")
		Expect(os.WriteFile(filepath.Join(work, "file.txt"), []byte("work\n"), 0o600)).To(Succeed())
		runGitIn(work, "add", "file.txt")
		runGitIn(work, "commit", "-q", "-m", "dispatched work")

		pushURL, err := gitagent.HTTPSRepoURL(fixture.server.URL, "repo.git")
		Expect(err).NotTo(HaveOccurred())
		env, err := gitagent.TransportEnv(os.Environ(), gitagent.TransportTarget{
			URL:    pushURL,
			Token:  fixture.secret,
			CAPath: fixture.credential.CertPath,
		})
		Expect(err).NotTo(HaveOccurred())

		cmd := exec.Command("git", "push", pushURL, "HEAD:refs/heads/dispatched")
		cmd.Dir = work
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		// The hook exits 1 on purpose, so the push is refused — but only after
		// the transport authenticated the peer and ran receive-pack.
		Expect(err).To(HaveOccurred())
		Expect(string(out)).To(ContainSubstring("pushed by " + sidecarPeer + " as " + string(gitagent.RoleSidecar)))
	})

	// A push with no credential must fail at the transport, not inside git, and
	// must leave no ref behind.
	It("leaves the repository untouched when the token is wrong", func() {
		fixture := newSidecarFixture()

		work := GinkgoT().TempDir()
		runGitIn(work, "init", "-q", "-b", "main", ".")
		Expect(os.WriteFile(filepath.Join(work, "file.txt"), []byte("work\n"), 0o600)).To(Succeed())
		runGitIn(work, "add", "file.txt")
		runGitIn(work, "commit", "-q", "-m", "dispatched work")

		pushURL, err := gitagent.HTTPSRepoURL(fixture.server.URL, "repo.git")
		Expect(err).NotTo(HaveOccurred())
		env, err := gitagent.TransportEnv(os.Environ(), gitagent.TransportTarget{
			URL:    pushURL,
			Token:  text.NewSensitiveString("cptn_aaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
			CAPath: fixture.credential.CertPath,
		})
		Expect(err).NotTo(HaveOccurred())

		cmd := exec.Command("git", "push", pushURL, "HEAD:refs/heads/dispatched")
		cmd.Dir = work
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		Expect(err).To(HaveOccurred(), "an unauthenticated push succeeded: %s", out)

		repo := filepath.Join(fixture.root, "repo.git")
		refs := runGitIn(repo, "for-each-ref", "--format=%(refname)")
		Expect(strings.TrimSpace(refs)).To(BeEmpty())
	})
})
