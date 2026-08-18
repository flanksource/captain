package gitagent_test

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

// gitHTTPFixture is a served root with one bare repository, fronted by the
// smart-HTTP transport over real TLS — the same shape a supervisor runs.
type gitHTTPFixture struct {
	root       string
	repoRoute  string
	server     *httptest.Server
	credential *gitagent.TLSCredential
	agent      string
	tokenPath  string
	// identifyErr makes the identity resolver fail, so a test can prove the
	// transport refuses an unattributable push before git ever runs.
	identifyErr error
}

func readAll(body io.Reader) string {
	GinkgoHelper()
	data, err := io.ReadAll(body)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

func newGitHTTPFixture() *gitHTTPFixture {
	GinkgoHelper()
	// A real mailbox route: ValidateMailboxRoute requires the sha256 of the
	// canonical repository, so a readable placeholder would be rejected before
	// the transport was ever reached.
	digest := sha256.Sum256([]byte("https://example.com/acme/project"))
	fixture := &gitHTTPFixture{
		root:      GinkgoT().TempDir(),
		repoRoute: "mailboxes/" + hex.EncodeToString(digest[:]) + ".git",
		agent:     "worker-01",
	}
	repo := filepath.Join(fixture.root, filepath.FromSlash(fixture.repoRoute))
	Expect(os.MkdirAll(repo, 0o755)).To(Succeed())
	runGitIn(repo, "init", "--bare", "-q", ".")
	// receive-pack refuses a push to the branch a bare repo has checked out.
	runGitIn(repo, "symbolic-ref", "HEAD", "refs/heads/placeholder")

	handler, err := gitagent.NewHTTPHandler(gitagent.HTTPServerConfig{
		Root: fixture.root,
		Role: gitagent.RoleMailbox,
		Identify: func(*http.Request) (string, error) {
			if fixture.identifyErr != nil {
				return "", fixture.identifyErr
			}
			return fixture.agent, nil
		},
	})
	Expect(err).NotTo(HaveOccurred())

	keysDir := GinkgoT().TempDir()
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

	fixture.tokenPath = filepath.Join(keysDir, gitagent.TokenFileName)
	Expect(gitagent.WriteTokenFile(fixture.tokenPath, text.NewSensitiveString("cptn_id.secret"))).To(Succeed())
	return fixture
}

// endpoint is the https://host:port form an agent is enrolled with.
func (f *gitHTTPFixture) endpoint() string { return f.server.URL }

func (f *gitHTTPFixture) client() *http.Client {
	GinkgoHelper()
	pem, err := f.credential.PEM()
	Expect(err).NotTo(HaveOccurred())
	pool := x509.NewCertPool()
	Expect(pool.AppendCertsFromPEM(pem)).To(BeTrue())
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
}

func runGitIn(dir string, args ...string) string {
	GinkgoHelper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=captain", "GIT_AUTHOR_EMAIL=captain@example.com",
		"GIT_COMMITTER_NAME=captain", "GIT_COMMITTER_EMAIL=captain@example.com")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %s: %s", strings.Join(args, " "), out)
	return string(out)
}

var _ = Describe("git smart-HTTP transport", func() {
	It("advertises receive-pack with the sideband the verdict feedback rides", func() {
		fixture := newGitHTTPFixture()

		response, err := fixture.client().Get(
			fixture.endpoint() + gitagent.GitHTTPPrefix + fixture.repoRoute + "/info/refs?service=git-receive-pack")
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		Expect(response.Header.Get("Content-Type")).To(Equal("application/x-git-receive-pack-advertisement"))
		Expect(response.Header.Get("Cache-Control")).To(ContainSubstring("no-cache"))

		body := readAll(response.Body)
		Expect(body).To(HavePrefix("001f# service=git-receive-pack\n0000"),
			"smart HTTP needs the service banner; without it git falls back to the dumb protocol")
		// The hooks write their verdict to receive-pack's sideband. If the
		// advertisement did not offer it the feedback would be silently lost.
		Expect(body).To(ContainSubstring("side-band-64k"))
	})

	// A shared upload-pack would leak every task namespace to every enrolled
	// agent, which is what R2.3 exists to prevent.
	It("refuses upload-pack and the dumb-protocol paths (R2.3)", func() {
		fixture := newGitHTTPFixture()
		base := fixture.endpoint() + gitagent.GitHTTPPrefix + fixture.repoRoute
		client := fixture.client()

		response, err := client.Get(base + "/info/refs?service=git-upload-pack")
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusForbidden))
		Expect(readAll(response.Body)).To(ContainSubstring("R2.3"))

		for _, path := range []string{
			"/git-upload-pack", "/HEAD", "/objects/info/packs", "/info/refs/../../etc/passwd",
		} {
			response, err := client.Get(base + path)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.StatusCode).To(BeNumerically(">=", http.StatusBadRequest),
				"%s must not be served", path)
			response.Body.Close()
		}
	})

	// R8.4: containment is checked after resolving, because stripping slashes
	// and quotes does not stop `..`.
	It("refuses a repository path that escapes the served root (R8.4)", func() {
		fixture := newGitHTTPFixture()

		response, err := fixture.client().Get(
			fixture.endpoint() + gitagent.GitHTTPPrefix + "../../../etc/info/refs?service=git-receive-pack")
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusNotFound))
	})

	// A push has to name an agent: the ref namespace R8.3 confines it to is
	// derived from that name, so an unidentified push has no namespace at all.
	It("refuses a push it cannot attribute to an agent", func() {
		fixture := newGitHTTPFixture()
		fixture.identifyErr = os.ErrPermission

		response, err := fixture.client().Get(
			fixture.endpoint() + gitagent.GitHTTPPrefix + fixture.repoRoute + "/info/refs?service=git-receive-pack")
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusForbidden))
		Expect(readAll(response.Body)).To(ContainSubstring("ref namespace"))
	})

	// The proof the whole path works: a real `git push` through the real client
	// transport, over TLS, authenticated by a token, landing a commit.
	It("accepts a real git push driven by the client transport", func() {
		fixture := newGitHTTPFixture()

		work := GinkgoT().TempDir()
		runGitIn(work, "init", "-q", "-b", "main", ".")
		Expect(os.WriteFile(filepath.Join(work, "file.txt"), []byte("work\n"), 0o600)).To(Succeed())
		runGitIn(work, "add", "file.txt")
		runGitIn(work, "commit", "-q", "-m", "agent work")

		pushURL, err := gitagent.MailboxURL(fixture.endpoint(), fixture.repoRoute)
		Expect(err).NotTo(HaveOccurred())
		Expect(pushURL).To(HavePrefix("https://"))
		Expect(pushURL).To(ContainSubstring(gitagent.GitHTTPPrefix + fixture.repoRoute))

		target := gitagent.RelayTarget{
			URL:       fixture.endpoint(),
			TokenPath: fixture.tokenPath,
			CAPath:    fixture.credential.CertPath,
			// Pinning is optional; exercising it here proves the value is in the
			// form git accepts, which a wrong encoding would fail on.
			PinnedPublicKey: fixture.credential.PublicKeyPin,
		}
		transport, err := target.Transport(pushURL)
		Expect(err).NotTo(HaveOccurred())
		env, err := gitagent.TransportEnv(os.Environ(), transport)
		Expect(err).NotTo(HaveOccurred())

		cmd := exec.Command("git", "push", pushURL, "HEAD:refs/heads/pushed")
		cmd.Dir = work
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "push failed: %s", out)

		repo := filepath.Join(fixture.root, filepath.FromSlash(fixture.repoRoute))
		Expect(runGitIn(repo, "log", "-1", "--format=%s", "refs/heads/pushed")).To(ContainSubstring("agent work"))
	})

	// The identity the transport resolved has to reach the hooks, because that
	// is the only thing the ref-namespace rules can be enforced against.
	It("injects the pushing agent into receive-pack's environment", func() {
		fixture := newGitHTTPFixture()
		repo := filepath.Join(fixture.root, filepath.FromSlash(fixture.repoRoute))
		hookPath := filepath.Join(repo, "hooks", "pre-receive")
		Expect(os.MkdirAll(filepath.Dir(hookPath), 0o755)).To(Succeed())
		Expect(os.WriteFile(hookPath, []byte(
			"#!/bin/sh\ncat >/dev/null\necho \"pushed by ${"+gitagent.EnvAgentName+"} as ${"+gitagent.EnvRole+"}\" >&2\nexit 1\n",
		), 0o755)).To(Succeed())

		work := GinkgoT().TempDir()
		runGitIn(work, "init", "-q", "-b", "main", ".")
		Expect(os.WriteFile(filepath.Join(work, "file.txt"), []byte("work\n"), 0o600)).To(Succeed())
		runGitIn(work, "add", "file.txt")
		runGitIn(work, "commit", "-q", "-m", "agent work")

		pushURL, err := gitagent.MailboxURL(fixture.endpoint(), fixture.repoRoute)
		Expect(err).NotTo(HaveOccurred())
		transport, err := gitagent.RelayTarget{
			URL: fixture.endpoint(), TokenPath: fixture.tokenPath, CAPath: fixture.credential.CertPath,
		}.Transport(pushURL)
		Expect(err).NotTo(HaveOccurred())
		env, err := gitagent.TransportEnv(os.Environ(), transport)
		Expect(err).NotTo(HaveOccurred())

		cmd := exec.Command("git", "push", pushURL, "HEAD:refs/heads/rejected")
		cmd.Dir = work
		cmd.Env = env
		out, _ := cmd.CombinedOutput()
		// The hook's stderr rides the sideband back to the client, which is how
		// a verdict reaches the agent unchanged over this transport.
		Expect(string(out)).To(ContainSubstring("pushed by worker-01 as mailbox"))
	})
})
