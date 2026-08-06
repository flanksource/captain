package gitagent_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gossh "golang.org/x/crypto/ssh"

	"github.com/flanksource/captain/pkg/gitagent"
)

// memoryDirectory is an in-process AgentDirectory for server tests.
type memoryDirectory struct {
	mu          sync.Mutex
	agents      map[string]string // fingerprint → name
	pending     map[string]string // token hash → name
	enrollments map[string]gitagent.AgentEnrollment
}

func (d *memoryDirectory) AgentByFingerprint(fp string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name, ok := d.agents[fp]
	return name, ok
}

func (d *memoryDirectory) ConsumeJoinToken(token string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name, ok := d.pending[gitagent.HashJoinToken(token)]
	if !ok {
		return "", fmt.Errorf("join token is unknown or already used")
	}
	delete(d.pending, gitagent.HashJoinToken(token))
	return name, nil
}

func (d *memoryDirectory) RecordAgent(e gitagent.AgentEnrollment) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.agents[e.Fingerprint] = e.Name
	if d.enrollments == nil {
		d.enrollments = map[string]gitagent.AgentEnrollment{}
	}
	d.enrollments[e.Name] = e
	return nil
}

// startTestServer serves root on a loopback port and returns its address and
// host fingerprint.
func startTestServer(dir *memoryDirectory, root string, role gitagent.ReceiverRole) (addr, hostFP string) {
	GinkgoHelper()
	return startTestServerWithOffer(dir, root, role, gitagent.EnrollmentOffer{})
}

func startTestServerWithOffer(dir *memoryDirectory, root string, role gitagent.ReceiverRole, offer gitagent.EnrollmentOffer) (addr, hostFP string) {
	GinkgoHelper()
	keys := GinkgoT().TempDir()
	hostKey, fp, err := gitagent.EnsureKeyPair(filepath.Join(keys, "host_ed25519"))
	Expect(err).NotTo(HaveOccurred())
	server, err := gitagent.NewServer(gitagent.ServerConfig{
		Root: root, Role: role, HostKey: hostKey, Directory: dir,
		Offer: offer, AgentRepoPath: "repo.git",
	})
	Expect(err).NotTo(HaveOccurred())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	go func() { _ = server.Serve(listener) }()
	DeferCleanup(func() { _ = server.Close() })
	return listener.Addr().String(), fp
}

func newClientKey() (gossh.Signer, string, string) {
	GinkgoHelper()
	keyPath := filepath.Join(GinkgoT().TempDir(), "agent_ed25519")
	signer, fp, err := gitagent.EnsureKeyPair(keyPath)
	Expect(err).NotTo(HaveOccurred())
	return signer, fp, keyPath
}

func sshExec(addr string, signer gossh.Signer, command string) (string, error) {
	config := &gossh.ClientConfig{
		User:            "captain",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	}
	client, err := gossh.Dial("tcp", addr, config)
	if err != nil {
		return "", err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	out, err := session.CombinedOutput(command)
	return string(out), err
}

var _ = Describe("the git-agent SSH endpoint", func() {
	ctx := context.Background()

	It("serves receive-pack to enrolled keys and completes a real push", func() {
		root := GinkgoT().TempDir()
		Expect(gitagent.InitSidecar(ctx, filepath.Join(root, "repo.git"))).To(Succeed())

		dir := &memoryDirectory{agents: map[string]string{}, pending: map[string]string{}}
		_, fp, keyPath := newClientKey()
		dir.agents[fp] = "worker-1"
		addr, hostFP := startTestServer(dir, root, gitagent.RoleSidecar)

		src := GinkgoT().TempDir()
		gitT(src, "init", "-q")
		writeFileT(src, "a.txt", "a\n")
		gitT(src, "add", "-A")
		gitT(src, "commit", "-q", "-m", "base")

		// The test binary doubles as GIT_SSH_COMMAND (see TestMain), so this
		// is the production transport end to end with no system ssh.
		exe, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		host, port, err := net.SplitHostPort(addr)
		Expect(err).NotTo(HaveOccurred())
		push := exec.Command("git", "push", "-q",
			fmt.Sprintf("ssh://captain@%s:%s/repo.git", host, port), "HEAD:refs/heads/pushed")
		push.Dir = src
		push.Env = append(os.Environ(),
			"GIT_SSH_COMMAND="+exe,
			"GIT_SSH_VARIANT=ssh", // an unknown command defaults to "simple", which cannot pass -p
			"CAPTAIN_TEST_SSH_CLIENT=1",
			gitagent.EnvSSHKey+"="+keyPath,
			gitagent.EnvSSHHostFingerprint+"="+hostFP,
		)
		out, err := push.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "push output:\n%s", out)

		Expect(gitT(filepath.Join(root, "repo.git"), "rev-parse", "refs/heads/pushed")).
			To(Equal(gitT(src, "rev-parse", "HEAD")))
	})

	It("refuses unknown keys, upload-pack, and path traversal", func() {
		root := GinkgoT().TempDir()
		Expect(gitagent.InitSidecar(ctx, filepath.Join(root, "repo.git"))).To(Succeed())
		dir := &memoryDirectory{agents: map[string]string{}, pending: map[string]string{}}
		enrolled, fp, _ := newClientKey()
		dir.agents[fp] = "worker-1"
		stranger, _, _ := newClientKey()
		addr, _ := startTestServer(dir, root, gitagent.RoleSidecar)

		out, err := sshExec(addr, stranger, "git-receive-pack 'repo.git'")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("not enrolled"))

		out, err = sshExec(addr, enrolled, "git-upload-pack 'repo.git'")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("git-receive-pack only"))

		out, err = sshExec(addr, enrolled, "git-receive-pack '../repo.git'")
		Expect(err).To(HaveOccurred())
		Expect(out).NotTo(BeEmpty())

		out, err = sshExec(addr, enrolled, "rm -rf /")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("not served"))
	})

	It("enrolls both directions through a single-use join token and refuses replay (R8.2)", func() {
		root := GinkgoT().TempDir()
		dir := &memoryDirectory{agents: map[string]string{}, pending: map[string]string{}}
		token, hash, err := gitagent.MintJoinToken()
		Expect(err).NotTo(HaveOccurred())
		dir.pending[hash] = "worker-2"
		addr, hostFP := startTestServerWithOffer(dir, root, gitagent.RoleMailbox,
			gitagent.EnrollmentOffer{DispatchKey: "SHA256:dispatch"})

		signer, fp, _ := newClientKey()
		request := gitagent.EnrollRequest{ListenPort: "7502", HostFingerprint: "SHA256:agenthost"}
		resp, err := gitagent.Enroll(context.Background(), "ssh://"+addr, token, hostFP, signer, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Agent).To(Equal("worker-2"))
		// The reverse direction: what the agent needs to accept a dispatch.
		// Repository-specific mailbox routing arrives with each task.
		Expect(resp.DispatchKey).To(Equal("SHA256:dispatch"))

		// The supervisor recorded an endpoint it can actually dispatch to.
		enrolled, ok := dir.enrollments["worker-2"]
		Expect(ok).To(BeTrue())
		Expect(enrolled.Fingerprint).To(Equal(fp))
		Expect(enrolled.HostFingerprint).To(Equal("SHA256:agenthost"))
		Expect(enrolled.URL).To(ContainSubstring(":7502/repo.git"))

		route := "mailboxes/" + strings.Repeat("a", 64) + ".git"
		mailboxURL, err := gitagent.MailboxURL("ssh://"+addr, route)
		Expect(err).NotTo(HaveOccurred())
		Expect(mailboxURL).To(HaveSuffix("/" + route))
		_, err = gitagent.MailboxURL("ssh://"+addr, "../other.git")
		Expect(err).To(MatchError(ContainSubstring("mailbox route")))

		// Replay fails: the token burned.
		_, err = gitagent.Enroll(context.Background(), "ssh://"+addr, token, hostFP, signer, request)
		Expect(err).To(MatchError(ContainSubstring("already used")))

		// A wrong host fingerprint is refused before the token is offered.
		_, err = gitagent.Enroll(context.Background(), "ssh://"+addr, token, "SHA256:bogus", signer, request)
		Expect(err).To(HaveOccurred())

		// An empty fingerprint never trusts on first use.
		_, err = gitagent.Enroll(context.Background(), "ssh://"+addr, token, "", signer, request)
		Expect(err).To(MatchError(ContainSubstring("host-key fingerprint")))
	})

	It("resolves repo paths strictly within the root (R8.4/H13)", func() {
		root := GinkgoT().TempDir()
		Expect(gitagent.InitSidecar(ctx, filepath.Join(root, "repo.git"))).To(Succeed())

		resolved, err := gitagent.ResolveRepoPath(root, "/repo.git")
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(HaveSuffix("repo.git"))

		_, err = gitagent.ResolveRepoPath(root, "'/repo.git'")
		Expect(err).NotTo(HaveOccurred(), "quoted forms are unquoted")

		for _, evil := range []string{"../outside.git", "/../outside.git", "a/../../outside.git", ""} {
			_, err := gitagent.ResolveRepoPath(root, evil)
			Expect(err).To(HaveOccurred(), "path %q must be rejected", evil)
		}
	})
})
