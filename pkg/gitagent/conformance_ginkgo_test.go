package gitagent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/gitagent"
)

// conformanceWorld is the §12 topology built from local repos and loopback
// SSH: a supervisor host (real repo + mailbox + serve) and an agent host
// (sidecar repo + serve), with the test binary standing in for the captain
// binary in both the shims and the ssh transport.
type conformanceWorld struct {
	superRepo  string
	mailbox    string
	sidecar    string
	sidecarURL string
	dispatch   gitagent.DispatchRequest
	workdir    string // the agent's clone, present after dispatch
}

// testShim writes a hook shim exec'ing the test binary with the hook env
// armed and the ssh env disarmed (each invocation re-arms its own).
func installTestShims(repo, role, runtimeCfg string) {
	GinkgoHelper()
	exe, err := os.Executable()
	Expect(err).NotTo(HaveOccurred())
	for _, hook := range []string{"pre-receive", "post-receive"} {
		shim := fmt.Sprintf("#!/bin/sh\nCAPTAIN_TEST_HOOK=1 CAPTAIN_TEST_SSH_CLIENT= exec %q %s %q %q %q\n",
			exe, hook, repo, role, runtimeCfg)
		path := filepath.Join(repo, "hooks", hook)
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(shim), 0o755)).To(Succeed())
	}
}

func writeRuntime(dir string, rt gitagent.HookRuntime) string {
	GinkgoHelper()
	data, err := json.Marshal(rt)
	Expect(err).NotTo(HaveOccurred())
	path := filepath.Join(dir, "runtime.json")
	Expect(os.WriteFile(path, data, 0o644)).To(Succeed())
	return path
}

// testSSHCommand is the GIT_SSH_COMMAND for pushes in the conformance world:
// the test binary with its ssh persona armed and the hook persona disarmed.
func testSSHCommand() string {
	GinkgoHelper()
	exe, err := os.Executable()
	Expect(err).NotTo(HaveOccurred())
	return fmt.Sprintf("env CAPTAIN_TEST_SSH_CLIENT=1 CAPTAIN_TEST_HOOK= %q", exe)
}

// newConformanceWorld wires the full topology. sidecarWF and supervisorWF are
// the two tier hook sets; agentCommand is launched detached on dispatch.
func newConformanceWorld(ctx context.Context, sidecarWF, supervisorWF *api.Workflow, agentCommand string) *conformanceWorld {
	GinkgoHelper()
	w := &conformanceWorld{}

	// Supervisor host: real repo with dirty state, mailbox beside it.
	supRoot := GinkgoT().TempDir()
	w.superRepo = filepath.Join(supRoot, "project")
	Expect(os.MkdirAll(w.superRepo, 0o755)).To(Succeed())
	gitT(w.superRepo, "init", "-q")
	writeFileT(w.superRepo, "pkg/main.go", "package main\n")
	writeFileT(w.superRepo, "docs/readme.md", "readme\n")
	gitT(w.superRepo, "add", "-A")
	gitT(w.superRepo, "commit", "-q", "-m", "base")
	writeFileT(w.superRepo, "pkg/dirty.go", "package main // dirty\n")
	w.mailbox = filepath.Join(supRoot, "mailbox.git")
	Expect(gitagent.InitMailbox(ctx, w.mailbox, w.superRepo)).To(Succeed())

	// Keys: one per party.
	supKey := filepath.Join(GinkgoT().TempDir(), "supervisor_ed25519")
	_, supFP, err := gitagent.EnsureKeyPair(supKey)
	Expect(err).NotTo(HaveOccurred())
	agentKey := filepath.Join(GinkgoT().TempDir(), "agent_ed25519")
	_, agentFP, err := gitagent.EnsureKeyPair(agentKey)
	Expect(err).NotTo(HaveOccurred())

	// Supervisor mailbox endpoint, accepting the enrolled agent's key.
	supDir := &memoryDirectory{agents: map[string]string{agentFP: "worker-1"}, pending: map[string]string{}}
	supAddr, supHostFP := startTestServer(supDir, supRoot, gitagent.RoleMailbox)
	supHost, supPort, err := net.SplitHostPort(supAddr)
	Expect(err).NotTo(HaveOccurred())

	// Agent host: sidecar repo + endpoint accepting the supervisor's key.
	agentRoot := GinkgoT().TempDir()
	w.sidecar = filepath.Join(agentRoot, "repo.git")
	Expect(gitagent.InitSidecar(ctx, w.sidecar)).To(Succeed())
	sideDir := &memoryDirectory{agents: map[string]string{supFP: "supervisor"}, pending: map[string]string{}}
	sideAddr, sideHostFP := startTestServer(sideDir, agentRoot, gitagent.RoleSidecar)
	sideHost, sidePort, err := net.SplitHostPort(sideAddr)
	Expect(err).NotTo(HaveOccurred())
	w.sidecarURL = fmt.Sprintf("ssh://captain@%s:%s/repo.git", sideHost, sidePort)

	// Hook runtimes and shims on both receivers.
	sidecarRT := writeRuntime(GinkgoT().TempDir(), gitagent.HookRuntime{
		SidecarWorkflow: sidecarWF,
		HookSandbox:     "test-identity",
		AgentCommand:    agentCommand,
		Relay: gitagent.RelayTarget{
			URL:             fmt.Sprintf("ssh://captain@%s:%s/mailbox.git", supHost, supPort),
			HostFingerprint: supHostFP,
			KeyPath:         agentKey,
			SSHCommand:      testSSHCommand(),
		},
	})
	installTestShims(w.sidecar, "sidecar", sidecarRT)
	mailboxRT := writeRuntime(GinkgoT().TempDir(), gitagent.HookRuntime{
		SupervisorWorkflow: supervisorWF,
		HookSandbox:        "test-identity",
		RealRepo:           w.superRepo,
	})
	installTestShims(w.mailbox, "mailbox", mailboxRT)

	w.dispatch = gitagent.DispatchRequest{
		RepoDir:       w.superRepo,
		MailboxPath:   w.mailbox,
		Agent:         "worker-1",
		SidecarURL:    w.sidecarURL,
		SidecarHostFP: sideHostFP,
		KeyPath:       supKey,
		SSHCommand:    testSSHCommand(),
		Policy:        gitagent.Policy{MaxAttempts: 5},
	}
	return w
}

// agentPush commits the given files in the agent's clone and pushes bare,
// returning the push's combined output and error.
func (w *conformanceWorld) agentPush(files map[string]string) (string, error) {
	GinkgoHelper()
	for path, content := range files {
		writeFileT(w.workdir, path, content)
	}
	gitT(w.workdir, "add", "-A")
	gitT(w.workdir, "commit", "-q", "-m", "agent work", "--allow-empty")
	push := exec.Command("git", "push")
	push.Dir = w.workdir
	out, err := push.CombinedOutput()
	return string(out), err
}

func (w *conformanceWorld) dispatchTask(ctx context.Context) *gitagent.DispatchResult {
	GinkgoHelper()
	result, err := gitagent.Dispatch(ctx, w.dispatch)
	Expect(err).NotTo(HaveOccurred())
	w.workdir = filepath.Join(w.sidecar, "captain", "tasks", result.Task, "worktree")
	return result
}

var _ = Describe("protocol conformance (§12)", Serial, func() {
	ctx := context.Background()

	It("completes a full cycle from a vanilla clone with bare git commands (H17)", func() {
		w := newConformanceWorld(ctx,
			&api.Workflow{Verify: &api.Verify{Commands: []string{"test -f pkg/ok.txt"}}},
			&api.Workflow{Verify: &api.Verify{Commands: []string{"grep -q good pkg/ok.txt"}}},
			"")
		result := w.dispatchTask(ctx)
		Expect(w.workdir).To(BeADirectory(), "post-receive must set up the agent workspace")
		Expect(os.ReadFile(filepath.Join(w.workdir, "pkg", "dirty.go"))).
			To(Equal([]byte("package main // dirty\n")), "the dirty worktree travelled")
		Expect(filepath.Join(w.sidecar, "captain", "tasks", result.Task, "task.json")).
			To(BeAnExistingFile(), "task.json lands outside the worktree")
		Expect(filepath.Join(w.workdir, "task.json")).NotTo(BeAnExistingFile())

		// HEAD moves on the supervisor mid-task: integration must use the
		// envelope's base, not the moved HEAD (R10.1).
		writeFileT(w.superRepo, "docs/readme.md", "readme v2\n")
		gitT(w.superRepo, "commit", "-q", "-am", "moved after dispatch")

		out, err := w.agentPush(map[string]string{"pkg/ok.txt": "good\n"})
		Expect(err).NotTo(HaveOccurred(), "push output:\n%s", out)
		Expect(out).To(ContainSubstring("captain: ACCEPTED"))

		verdict, err := gitagent.AwaitOutcome(ctx, w.mailbox, result.Task, 30*time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(verdict.Status).To(Equal(gitagent.StatusAccepted))

		// Result + control landed in the mailbox; the verdict ref exists.
		Expect(gitT(w.mailbox, "rev-parse", "refs/captain/tasks/"+result.Task+"/result/1")).NotTo(BeEmpty())
		Expect(gitT(w.mailbox, "rev-parse", "refs/captain/tasks/"+result.Task+"/verdict/1")).NotTo(BeEmpty())

		// Integration: the captain/<task> branch carries BOTH the agent's file
		// and the supervisor's post-dispatch commit.
		branch := "captain/" + result.Task
		Expect(blob(w.superRepo, branch, "pkg/ok.txt")).To(Equal("good\n"))
		Expect(blob(w.superRepo, branch, "docs/readme.md")).To(Equal("readme v2\n"))
		Expect(blob(w.superRepo, branch, "pkg/dirty.go")).To(Equal("package main // dirty\n"))
	})

	It("rejects at tier 1 with feedback, never contacting the supervisor, and recovers on retry (§6.3)", func() {
		w := newConformanceWorld(ctx,
			&api.Workflow{Verify: &api.Verify{Commands: []string{"test -f pkg/ok.txt"}}},
			nil, "")
		result := w.dispatchTask(ctx)

		refsBefore := gitT(w.sidecar, "for-each-ref")
		out, err := w.agentPush(map[string]string{"pkg/other.txt": "not the file\n"})
		Expect(err).To(HaveOccurred(), "push must be rejected:\n%s", out)
		Expect(out).To(ContainSubstring("captain: REJECTED"), "tier-1 feedback reaches the pusher")
		Expect(out).To(ContainSubstring("verify:test -f pkg/ok.txt"))
		Expect(out).To(ContainSubstring("captain-json: "))

		// The supervisor was never contacted: no result refs, no verdicts.
		Expect(gitT(w.mailbox, "for-each-ref", "refs/captain/tasks/"+result.Task+"/result")).To(BeEmpty())
		_, found, err := gitagent.LoadVerdict(w.mailbox, result.Task, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		// Zero new refs at the sidecar; the remote task branch did not advance.
		Expect(gitT(w.sidecar, "for-each-ref")).To(Equal(refsBefore))

		// The rejection persisted out-of-band at the rejecting tier (R6.9).
		verdict, found, err := gitagent.LoadVerdict(w.sidecar, result.Task, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(verdict.Status).To(Equal(gitagent.StatusRejected))

		// Rejection is not termination: fix and push again as attempt 2.
		out, err = w.agentPush(map[string]string{"pkg/ok.txt": "fixed\n"})
		Expect(err).NotTo(HaveOccurred(), "retry output:\n%s", out)
		Expect(out).To(ContainSubstring("captain: ACCEPTED"))
		Expect(gitT(w.mailbox, "rev-parse", "refs/captain/tasks/"+result.Task+"/result/2")).NotTo(BeEmpty())
	})

	It("relays tier-2 rejection text down through the sidecar (H16)", func() {
		w := newConformanceWorld(ctx,
			nil,
			&api.Workflow{Verify: &api.Verify{Commands: []string{"grep -q good pkg/ok.txt"}}},
			"")
		result := w.dispatchTask(ctx)

		out, err := w.agentPush(map[string]string{"pkg/ok.txt": "bad\n"})
		Expect(err).To(HaveOccurred(), "push must be rejected:\n%s", out)
		Expect(out).To(ContainSubstring("verify:grep -q good pkg/ok.txt"), "the supervisor's feedback travelled the sideband chain")

		// The supervisor's rejection persisted at its tier (R6.9); quarantine
		// left no result ref behind.
		verdict, found, err := gitagent.LoadVerdict(w.mailbox, result.Task, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(verdict.Status).To(Equal(gitagent.StatusRejected))
		Expect(verdict.Tier).To(Equal("supervisor"))
		Expect(gitT(w.mailbox, "for-each-ref", "refs/captain/tasks/"+result.Task+"/result")).To(BeEmpty())

		// The agent's local branch stays intact and ahead.
		Expect(gitT(w.workdir, "rev-list", "--count", "origin/captain/"+result.Task+"..HEAD")).To(Equal("1"))
	})

	It("returns from dispatch promptly while the agent keeps running (H12)", func() {
		w := newConformanceWorld(ctx, nil, nil, "echo started > agent-marker.txt && sleep 30")
		start := time.Now()
		result := w.dispatchTask(ctx)
		Expect(time.Since(start)).To(BeNumerically("<", 15*time.Second),
			"the dispatch push must not wait for the agent")
		marker := filepath.Join(w.workdir, "agent-marker.txt")
		Eventually(func() error { _, err := os.Stat(marker); return err }, "10s", "200ms").Should(Succeed(),
			"the detached agent runs on after the push returned")
		Expect(strings.TrimSpace(string(mustRead(marker)))).To(Equal("started"))
		_ = result
	})
})

func mustRead(path string) []byte {
	GinkgoHelper()
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return data
}
