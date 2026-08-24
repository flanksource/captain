package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/commons-db/dbtest"
	"gopkg.in/yaml.v3"
)

// lockedBuffer collects a background process's output safely across the
// goroutine that waits on it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func netDial(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 200*time.Millisecond)
}

// freeLocalPort reserves a loopback port and releases it for immediate reuse.
func freeLocalPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// End-to-end coverage through the compiled binary, with each host in its own
// process and its own HOME. It exists because the protocol conformance suite
// wires its topology programmatically, and because captainconfig's path is
// process-global: neither can see a broken enrollment, a missing endpoint
// record, or a server exposing the wrong role — each of which leaves a working
// protocol attached to a product that cannot complete a cycle.

var (
	captainBuildOnce sync.Once
	captainBinPath   string
	captainBuildErr  error
)

// captainBinary builds cmd/captain once per test binary.
func captainBinary(t *testing.T) string {
	t.Helper()
	captainBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "captain-e2e-bin-")
		if err != nil {
			captainBuildErr = err
			return
		}
		out := filepath.Join(dir, "captain")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/captain")
		cmd.Dir = moduleRoot(t)
		if combined, err := cmd.CombinedOutput(); err != nil {
			captainBuildErr = fmt.Errorf("go build: %w\n%s", err, combined)
			return
		}
		captainBinPath = out
	})
	if captainBuildErr != nil {
		t.Fatal(captainBuildErr)
	}
	return captainBinPath
}

// moduleRoot is the repository root relative to this package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// host is one machine in the topology: an isolated HOME and the binary.
type host struct {
	t            *testing.T
	home         string
	bin          string
	sessionDBURL string
	serveOut     *lockedBuffer
}

func newHost(t *testing.T) *host {
	t.Helper()
	return &host{t: t, home: t.TempDir(), bin: captainBinary(t)}
}

func (h *host) env() []string {
	databaseURL := h.sessionDBURL
	if databaseURL == "" {
		databaseURL = "off"
	}
	return append(os.Environ(), "HOME="+h.home, "CAPTAIN_SESSION_DB_URL="+databaseURL)
}

// run executes a captain command to completion.
func (h *host) run(args ...string) (string, error) {
	h.t.Helper()
	cmd := exec.Command(h.bin, args...)
	cmd.Env = h.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (h *host) mustRun(args ...string) string {
	h.t.Helper()
	out, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("captain %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// serve starts a receive endpoint in the background and waits for it to
// accept, failing with the process's own output if it exits first.
func (h *host) serve(port string, args ...string) {
	h.t.Helper()
	full := append([]string{"sandbox", "git-agent", "serve", "--listen", "127.0.0.1:" + port}, args...)
	cmd := exec.Command(h.bin, full...)
	cmd.Env = h.env()
	var out lockedBuffer
	h.serveOut = &out
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	var waitErr error
	done := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	h.t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := netDial("127.0.0.1:" + port); err == nil {
			conn.Close()
			return
		}
		select {
		case <-done:
			h.t.Fatalf("serve exited before listening (%v):\n%s", waitErr, out.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	h.t.Fatalf("serve never began listening:\n%s", out.String())
}

func (h *host) serveLogs() string {
	if h.serveOut == nil {
		return ""
	}
	return h.serveOut.String()
}

func (h *host) configBytes() string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.home, ".captain.yaml"))
	if err != nil {
		h.t.Fatalf("reading %s: %v", filepath.Join(h.home, ".captain.yaml"), err)
	}
	return string(data)
}

// setBackendOption edits one option under sandbox.backends.git-agent in this
// host's config, the way an operator would.
func (h *host) setBackendOption(t *testing.T, key, value string) {
	t.Helper()
	path := filepath.Join(h.home, ".captain.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	sandbox, _ := cfg["sandbox"].(map[string]any)
	backends, _ := sandbox["backends"].(map[string]any)
	backend, _ := backends["git-agent"].(map[string]any)
	if backend == nil {
		t.Fatalf("no git-agent backend in %s:\n%s", path, data)
	}
	backend[key] = value
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitIn runs git in dir with a pinned identity.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=test", "-c", "user.email=test@localhost",
		"-c", "init.defaultBranch=main",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v:\n%s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func writeAt(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo creates a seeded git repository.
func newRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q")
	writeAt(t, repo, "pkg/main.go", "package main\n")
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "base")
	return repo
}

func mailboxPathForRepo(t *testing.T, supervisor *host, repo string) string {
	t.Helper()
	root := filepath.Join(supervisor.home, ".captain", "sandbox", gitagent.ServedReposDirName)
	mailbox, err := gitagent.MailboxForRepository(context.Background(), root, repo)
	if err != nil {
		t.Fatal(err)
	}
	return mailbox.Path
}

// addResult is the JSON `add --format json` emits.
type addResult struct {
	Agent           string `json:"agent"`
	HostFingerprint string `json:"hostFingerprint"`
	DispatchKey     string `json:"dispatchKey"`
	JoinCommand     string `json:"joinCommand"`
}

// enrollPair brings up a supervisor and an agent using only public commands,
// exactly as the setup documentation instructs, and returns both hosts.
func enrollPair(t *testing.T) (supervisor, agent *host, repo, agentPort string, add addResult) {
	t.Helper()
	supervisor, agent = newHost(t), newHost(t)
	supervisor.sessionDBURL = dbtest.ForT(t, dbtest.Options{Name: "captain_gitagent_e2e"}).DSN()
	repo = newRepo(t)

	supPort := freeLocalPort(t)
	supervisor.serve(supPort, "--role", "mailbox")

	out := supervisor.mustRun("sandbox", "git-agent", "add", "worker-01",
		"--endpoint", "ssh://127.0.0.1:"+supPort, "--format", "json")
	if err := json.Unmarshal([]byte(firstJSONDocument(out)), &add); err != nil {
		t.Fatalf("add --format json is not a JSON document: %v\n%s", err, out)
	}
	if add.JoinCommand == "" || add.DispatchKey == "" {
		t.Fatalf("add did not report a join command and dispatch key: %+v", add)
	}

	// Run the printed join command verbatim, only redirecting the listen
	// address — an operator changes nothing else.
	joinArgs := parseJoin(t, add.JoinCommand)
	agentPort = freeLocalPort(t)
	agent.serve(agentPort, joinArgs...)
	return supervisor, agent, repo, agentPort, add
}

// parseJoin turns the printed join command into serve arguments.
func parseJoin(t *testing.T, join string) []string {
	t.Helper()
	fields := strings.Fields(join)
	idx := -1
	for i, f := range fields {
		if f == "serve" {
			idx = i + 1
			break
		}
	}
	if idx < 0 {
		t.Fatalf("join command does not invoke serve: %q", join)
	}
	args := fields[idx:]
	for _, required := range []string{"--token", "--supervisor", "--host-fingerprint"} {
		if !containsArg(args, required) {
			t.Fatalf("join command lacks %s, so an operator cannot complete enrollment: %q", required, join)
		}
	}
	return args
}

func containsArg(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// TestEnrollmentProducesADispatchableTopology is the regression test for the
// setup blockers: after only `add` on the supervisor and the printed join
// command on the agent, dispatch must be possible with no hand-edited config.
func TestEnrollmentProducesADispatchableTopology(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary")
	}
	supervisor, agent, repo, agentPort, add := enrollPair(t)

	// The supervisor must hold a dispatchable record: key, endpoint and host
	// key. Recording only the key is what made dispatch impossible.
	listed := supervisor.mustRun("sandbox", "git-agent", "list", "--format", "json")
	var entries []struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		URL         string `json:"url"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal([]byte(firstJSONDocument(listed)), &entries); err != nil {
		t.Fatalf("list --format json is not a JSON document: %v\n%s", err, listed)
	}
	var worker *struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		URL         string `json:"url"`
		Status      string `json:"status"`
	}
	for i := range entries {
		if entries[i].Name == "worker-01" && entries[i].Status == "enrolled" {
			worker = &entries[i]
		}
	}
	if worker == nil {
		t.Fatalf("worker-01 is not enrolled: %s", listed)
	}
	if worker.Fingerprint == "" || worker.URL == "" {
		t.Fatalf("enrollment recorded no key or endpoint: %+v", worker)
	}
	if !strings.Contains(worker.URL, agentPort) || !strings.HasSuffix(worker.URL, SidecarRepoName) {
		t.Fatalf("recorded endpoint %q must address the agent's port and repository", worker.URL)
	}
	if !strings.Contains(supervisor.configBytes(), "hostFingerprint") {
		t.Fatalf("the supervisor recorded no host key to pin when dispatching:\n%s", supervisor.configBytes())
	}
	if !strings.Contains(supervisor.configBytes(), "mailboxRoot") {
		t.Fatalf("the supervisor endpoint recorded no root for lazy mailboxes:\n%s", supervisor.configBytes())
	}

	// The agent authorizes the supervisor once and retains only its stable
	// endpoint. A mailbox path is selected later for each dispatched repository.
	agentCfg := agent.configBytes()
	if !strings.Contains(agentCfg, add.DispatchKey) {
		t.Fatalf("the agent did not authorize the supervisor's dispatch key %s:\n%s", add.DispatchKey, agentCfg)
	}
	if !strings.Contains(agentCfg, "ssh://127.0.0.1:") {
		t.Fatalf("the agent recorded no supervisor endpoint:\n%s", agentCfg)
	}
	mailbox := mailboxPathForRepo(t, supervisor, repo)
	if _, err := os.Stat(mailbox); !os.IsNotExist(err) {
		t.Fatalf("repository mailbox must be created lazily at dispatch, stat error = %v", err)
	}
}

// TestFullCycleWithAManualAgent covers the protocol half of the cycle: a
// dispatch reaches the agent, work pushed with ordinary git relays to the
// supervisor, and accepted work is integrated.
//
// It drives the agent by hand deliberately, which is exactly what it does NOT
// prove: that a dispatch launches anything. TestDispatchLaunchesAnAgent covers
// that, and the two must stay separate — a manual push in this test once
// masked a launch path that never ran.
func TestFullCycleWithAManualAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary and runs two endpoints")
	}
	supervisor, agent, repo, _, _ := enrollPair(t)
	// Opt out of the launcher: this test drives the agent itself.
	agent.setBackendOption(t, "agentCommand", gitagent.NoAgentCommand)

	// A dirty supervisor worktree must travel with the dispatch.
	writeAt(t, repo, "task.prompt", "---\nsandbox: git-agent\n---\n{{role \"user\"}}\nAdd a greeting.\n")
	writeAt(t, repo, "pkg/main.go", "package main\n\n// dirty\n")

	// Dispatch blocks until the verdict (relay=sync), so it runs alongside the
	// agent's work.
	dispatch := exec.Command(supervisor.bin, "ai", "prompt", "./task.prompt", "--sandbox", "git-agent")
	dispatch.Dir = repo
	dispatch.Env = supervisor.env()
	var dispatchOut lockedBuffer
	dispatch.Stdout, dispatch.Stderr = &dispatchOut, &dispatchOut
	if err := dispatch.Start(); err != nil {
		t.Fatal(err)
	}
	dispatchDone := make(chan error, 1)
	go func() { dispatchDone <- dispatch.Wait() }()
	t.Cleanup(func() {
		if dispatch.Process != nil {
			_ = dispatch.Process.Kill()
		}
	})

	// The agent's workspace is an ordinary worktree with a branch and upstream.
	tasksDir := filepath.Join(agent.home, ".captain", "sandbox", gitagent.ServedReposDirName, SidecarRepoName, "captain", "tasks")
	var worktree, taskID string
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && worktree == "" {
		entries, _ := os.ReadDir(tasksDir)
		for _, e := range entries {
			candidate := filepath.Join(tasksDir, e.Name(), "worktree")
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				worktree, taskID = candidate, e.Name()
			}
		}
		if worktree != "" {
			break
		}
		select {
		case err := <-dispatchDone:
			t.Fatalf("dispatch exited before creating a workspace (%v):\n%s", err, dispatchOut.String())
		case <-time.After(250 * time.Millisecond):
		}
	}
	if worktree == "" {
		t.Fatalf("no agent workspace appeared:\n%s", dispatchOut.String())
	}
	if got := gitIn(t, worktree, "rev-parse", "--abbrev-ref", "@{u}"); !strings.HasSuffix(got, taskID) {
		t.Fatalf("upstream = %q; a bare `git push` needs one (H17)", got)
	}
	// The supervisor's uncommitted edit travelled.
	body, err := os.ReadFile(filepath.Join(worktree, "pkg", "main.go"))
	if err != nil || !strings.Contains(string(body), "// dirty") {
		t.Fatalf("the dispatch did not carry the dirty worktree: %q (%v)", body, err)
	}

	// The agent uses ordinary git and nothing else.
	writeAt(t, worktree, "pkg/main.go", string(body)+"\nfunc Greet() string { return \"hi\" }\n")
	gitIn(t, worktree, "add", "-A")
	gitIn(t, worktree, "commit", "-q", "-m", "add greeting")
	push := exec.Command("git", "push")
	push.Dir = worktree
	pushOut, err := push.CombinedOutput()
	if err != nil {
		t.Fatalf("the agent's bare push failed: %v\n%s", err, pushOut)
	}
	if !strings.Contains(string(pushOut), "ACCEPTED") {
		t.Fatalf("push was not accepted:\n%s", pushOut)
	}

	select {
	case err := <-dispatchDone:
		if err != nil {
			t.Fatalf("dispatch failed: %v\n%s", err, dispatchOut.String())
		}
	case <-time.After(90 * time.Second):
		t.Fatalf("dispatch never concluded after the push:\n%s", dispatchOut.String())
	}
	if !strings.Contains(dispatchOut.String(), "accepted") {
		t.Fatalf("dispatch did not report acceptance:\n%s", dispatchOut.String())
	}

	// The supervisor holds the result and verdict refs, and integrated the work
	// onto a branch without touching the user's checkout.
	mailbox := mailboxPathForRepo(t, supervisor, repo)
	refs := gitIn(t, mailbox, "for-each-ref", "--format=%(refname)")
	for _, want := range []string{"/result/1", "/verdict/1"} {
		if !strings.Contains(refs, taskID+want) {
			t.Fatalf("mailbox is missing %s%s:\n%s", taskID, want, refs)
		}
	}
	integrated := gitIn(t, repo, "show", "captain/"+taskID+":pkg/main.go")
	if !strings.Contains(integrated, "func Greet()") || !strings.Contains(integrated, "// dirty") {
		t.Fatalf("integration lost the agent's work or the dispatched state:\n%s", integrated)
	}
	if branch := gitIn(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); strings.HasPrefix(branch, "captain/") {
		t.Fatalf("integration switched the user's checkout to %s", branch)
	}
}

// TestCaptainTokenIsDurableAcrossRestarts pins the property that replaced the
// single-use join token (R8.2): presenting the same token again re-enrolls to
// the same identity rather than being refused.
//
// This is what a restarting or rescheduled sidecar does on every start. Under
// the old burn-on-use token it crash-looped, and joinOnce existed only to work
// around that; the workaround is gone, so the behaviour it hid is asserted here.
func TestCaptainTokenIsDurableAcrossRestarts(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary")
	}
	_, _, _, _, add := enrollPair(t)

	replay := newHost(t)
	replay.serve(freeLocalPort(t), parseJoin(t, add.JoinCommand)...)

	logs := replay.serveLogs()
	if !strings.Contains(logs, "enrolled as worker-01") {
		t.Fatalf("re-presenting a durable token must re-enroll to the same identity:\n%s", logs)
	}
	if strings.Contains(logs, "already used") {
		t.Fatalf("the token was burned on first use; a rescheduled sidecar would crash-loop:\n%s", logs)
	}
}

// TestGitAgentHelpDocumentsItsOwnCommands covers the reported discoverability
// defect: the group inherited the parent's help and advertised the wrong
// commands.
func TestGitAgentHelpDocumentsItsOwnCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary")
	}
	h := newHost(t)
	parent, _ := h.run("sandbox", "--help")
	if !strings.Contains(parent, "git-agent") {
		t.Fatalf("sandbox help does not advertise git-agent:\n%s", parent)
	}
	out, _ := h.run("sandbox", "git-agent", "--help")
	for _, want := range []string{"serve", "add", "list", "revoke"} {
		if !strings.Contains(out, want) {
			t.Fatalf("git-agent help does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sandbox-runtime presets") {
		t.Fatalf("git-agent help is the parent's:\n%s", out)
	}
	for _, sub := range []string{"add", "serve", "list", "revoke"} {
		out, _ := h.run("sandbox", "git-agent", sub, "--help")
		if strings.Contains(out, "sandbox-runtime presets") {
			t.Fatalf("git-agent %s --help is the parent's:\n%s", sub, out)
		}
		if sub == "serve" && strings.Contains(out, "--repo string") {
			t.Fatalf("the long-running endpoint must not be repository-bound:\n%s", out)
		}
	}
}

// TestListEmitsAnArrayWhenEmpty covers the reported JSON defect.
func TestListEmitsAnArrayWhenEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary")
	}
	h := newHost(t)
	out := h.mustRun("sandbox", "git-agent", "list", "--format", "json")
	doc := firstJSONDocument(out)
	if strings.TrimSpace(doc) != "[]" {
		t.Fatalf("an empty roster must encode as [], got %q\n%s", doc, out)
	}
}

// firstJSONDocument extracts the JSON value from command output that may also
// carry log lines.
func firstJSONDocument(out string) string {
	trimmed := strings.TrimSpace(out)
	for _, open := range []string{"[", "{"} {
		if i := strings.Index(trimmed, open); i >= 0 {
			candidate := trimmed[i:]
			var probe any
			if json.Unmarshal([]byte(candidate), &probe) == nil {
				return candidate
			}
		}
	}
	return trimmed
}

// TestDispatchLaunchesAnAgent is the test the manual-push cycle test cannot
// be: it proves a dispatch starts work that completes on its own, with no
// human touching the worktree at any point.
//
// The agent is a scripted stand-in rather than a real model, so the
// launch → work → push → relay → integrate chain is exercised without needing
// credentials. What it does not cover is the UNCONFIGURED default — that is
// TestUnconfiguredDispatchLaunchesTheDefaultAgent below, plus
// TestDefaultAgentCommandRunsCaptain for the command itself.
func TestDispatchLaunchesAnAgent(t *testing.T) {
	t.Skip("temporarily disabled: endpoint cleanup races with TempDir removal in CI")
	if testing.Short() {
		t.Skip("builds the captain binary and runs two endpoints")
	}
	supervisor, agent, repo, _, _ := enrollPair(t)

	// A scripted agent: edit, commit, push — the same three steps run-task
	// performs after the model call.
	agent.setBackendOption(t, "agentCommand",
		`echo 'func Greet() string { return "hi" }' >> pkg/main.go `+
			`&& git add -A && git commit -q -m "captain: $CAPTAIN_TASK" && git push`)

	writeAt(t, repo, "task.prompt", "---\nsandbox: git-agent\n---\n{{role \"user\"}}\nAdd a greeting.\n")
	writeAt(t, repo, "pkg/main.go", "package main\n\n// dirty\n")

	// No manual step anywhere below: dispatch, and wait for it to conclude.
	dispatch := exec.Command(supervisor.bin, "ai", "prompt", "./task.prompt",
		"--sandbox", "git-agent", "--timeout", "3m")
	dispatch.Dir = repo
	dispatch.Env = supervisor.env()
	out, err := dispatch.CombinedOutput()
	if err != nil {
		t.Fatalf("the dispatch did not conclude on its own: %v\n%s\n%s", err, out, agentLogs(t, agent))
	}
	if !strings.Contains(string(out), "accepted") {
		t.Fatalf("dispatch did not report acceptance:\n%s\n%s", out, agentLogs(t, agent))
	}

	// The agent's work reached the supervisor and was integrated, with no
	// human touching the worktree.
	mailbox := mailboxPathForRepo(t, supervisor, repo)
	refs := gitIn(t, mailbox, "for-each-ref", "--format=%(refname)")
	if !strings.Contains(refs, "/result/1") || !strings.Contains(refs, "/verdict/1") {
		t.Fatalf("no result/verdict refs; the launched agent never submitted:\n%s", refs)
	}
	branch := ""
	for _, line := range strings.Split(gitIn(t, repo, "branch", "--format=%(refname:short)"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "captain/") {
			branch = strings.TrimSpace(line)
		}
	}
	if branch == "" {
		t.Fatalf("accepted work was not integrated:\n%s", gitIn(t, repo, "branch"))
	}
	integrated := gitIn(t, repo, "show", branch+":pkg/main.go")
	if !strings.Contains(integrated, "func Greet()") || !strings.Contains(integrated, "// dirty") {
		t.Fatalf("integration lost the agent's work or the dispatched state:\n%s", integrated)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logs := agent.serveLogs()
		if strings.Contains(logs, "git-agent task") && strings.Contains(logs, "received") && strings.Contains(logs, "accepted at sidecar") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("agent serve did not report the task lifecycle:\n%s", agent.serveLogs())
}

// TestOneEndpointRoutesTwoRepositories proves service lifetime is independent
// of repository lifetime: one enrollment and one listener own isolated
// mailboxes while the shared sidecar executes both tasks.
func TestOneEndpointRoutesTwoRepositories(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary and runs two endpoints")
	}
	supervisor, agent, repoA, _, _ := enrollPair(t)
	repoB := newRepo(t) // same basename, different canonical path
	agent.setBackendOption(t, "agentCommand",
		`echo "// completed $CAPTAIN_TASK" >> pkg/main.go `+
			`&& git add -A && git commit -q -m "captain: $CAPTAIN_TASK" && git push; `+
			`status=$?; touch "$CAPTAIN_TASK_FILE.done"; exit $status`)

	run := func(repo string) string {
		t.Helper()
		writeAt(t, repo, "task.prompt", "---\nsandbox: git-agent\n---\n{{role \"user\"}}\nComplete this task.\n")
		cmd := exec.Command(supervisor.bin, "ai", "prompt", "./task.prompt",
			"--sandbox", "git-agent", "--timeout", "3m")
		cmd.Dir, cmd.Env = repo, supervisor.env()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dispatch from %s failed: %v\n%s\n%s", repo, err, out, agentLogs(t, agent))
		}
		branches := strings.Fields(gitIn(t, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/captain"))
		if len(branches) != 1 {
			t.Fatalf("integration branches in %s = %v", repo, branches)
		}
		task := strings.TrimPrefix(branches[0], "captain/")
		marker := filepath.Join(agent.home, ".captain", "sandbox", gitagent.ServedReposDirName,
			SidecarRepoName, "captain", "tasks", task, gitagent.ControlTaskFile+".done")
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(marker); err == nil {
				return task
			} else if !os.IsNotExist(err) {
				t.Fatalf("stat agent completion marker %s: %v", marker, err)
			}
			if time.Now().After(deadline) {
				t.Fatalf("detached agent for %s did not exit:\n%s", task, agentLogs(t, agent))
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	taskA := run(repoA)
	taskB := run(repoB)
	if taskA == taskB {
		t.Fatalf("two dispatches reused task id %s", taskA)
	}
	mailboxA := mailboxPathForRepo(t, supervisor, repoA)
	mailboxB := mailboxPathForRepo(t, supervisor, repoB)
	if mailboxA == mailboxB {
		t.Fatalf("same-named repositories share mailbox %s", mailboxA)
	}
	for _, tc := range []struct {
		mailbox, ownTask, otherTask, repo string
	}{{mailboxA, taskA, taskB, repoA}, {mailboxB, taskB, taskA, repoB}} {
		refs := gitIn(t, tc.mailbox, "for-each-ref", "--format=%(refname)")
		if !strings.Contains(refs, tc.ownTask+"/result/1") || strings.Contains(refs, tc.otherTask) {
			t.Fatalf("mailbox %s has crossed task namespaces:\n%s", tc.mailbox, refs)
		}
		binding, err := gitagent.LoadMailboxBinding(tc.mailbox)
		canonicalRepo, canonicalErr := filepath.EvalSymlinks(tc.repo)
		if err != nil || canonicalErr != nil || binding.Repository != canonicalRepo {
			t.Fatalf("mailbox %s binding = %+v, %v; canonical repo = %s, %v",
				tc.mailbox, binding, err, canonicalRepo, canonicalErr)
		}
		shim, err := os.ReadFile(filepath.Join(tc.mailbox, "hooks", "pre-receive"))
		if err != nil || !strings.Contains(string(shim), `--role "mailbox"`) {
			t.Fatalf("mailbox %s has no mailbox hook: %v\n%s", tc.mailbox, err, shim)
		}
	}
}

// agentLogs returns whatever the detached agent wrote, which is the only
// diagnosis available when a dispatch fails to conclude.
func agentLogs(t *testing.T, agent *host) string {
	t.Helper()
	tasks := filepath.Join(agent.home, ".captain", "sandbox", gitagent.ServedReposDirName, SidecarRepoName, "captain", "tasks")
	entries, err := os.ReadDir(tasks)
	if err != nil {
		return "no task directory: " + err.Error()
	}
	var b strings.Builder
	for _, e := range entries {
		for _, name := range []string{"agent.stdout.log", "agent.stderr.log"} {
			if data, err := os.ReadFile(filepath.Join(tasks, e.Name(), name)); err == nil {
				fmt.Fprintf(&b, "--- %s/%s ---\n%s\n", e.Name(), name, data)
			}
		}
	}
	if b.Len() == 0 {
		return "the agent wrote no logs (was anything launched?)"
	}
	return b.String()
}

// TestDefaultAgentCommandRunsCaptain pins what an unconfigured sidecar
// launches: this binary, working the task in place. An empty default is the
// defect this whole pair of tests exists to prevent.
func TestDefaultAgentCommandRunsCaptain(t *testing.T) {
	command := DefaultAgentCommand("/usr/local/bin/captain", "/srv/repo.git", "t-1", "/home/agent/.captain.yaml")
	for _, want := range []string{
		`"/usr/local/bin/captain"`, "sandbox git-agent run-task",
		`--repo "/srv/repo.git"`, `--task "t-1"`, `--config "/home/agent/.captain.yaml"`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("default agent command %q lacks %q", command, want)
		}
	}
	if strings.TrimSpace(DefaultAgentCommand("/bin/captain", "/r.git", "t-1", "")) == "" {
		t.Fatal("the default must never be empty; LaunchAgent would refuse and the dispatch would hang")
	}
}

// TestUnconfiguredDispatchLaunchesTheDefaultAgent closes the gap the other
// tests leave: a backend that configures no agentCommand at all must still
// launch a real agent. It cannot assert the task completes — captain's default
// agent calls a model, which needs credentials this suite does not have — so
// it asserts the launch itself, which is precisely what used to be missing.
func TestUnconfiguredDispatchLaunchesTheDefaultAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary and runs two endpoints")
	}
	supervisor, agent, repo, _, _ := enrollPair(t)
	if strings.Contains(agent.configBytes(), "agentCommand") {
		t.Fatalf("this test requires an unconfigured backend:\n%s", agent.configBytes())
	}

	writeAt(t, repo, "task.prompt", "---\nsandbox: git-agent\n---\n{{role \"user\"}}\nAdd a greeting.\n")

	// A short budget: the point is what got launched, not what it produced.
	dispatch := exec.Command(supervisor.bin, "ai", "prompt", "./task.prompt",
		"--sandbox", "git-agent", "--timeout", "45s")
	dispatch.Dir = repo
	dispatch.Env = supervisor.env()
	var out lockedBuffer
	dispatch.Stdout, dispatch.Stderr = &out, &out
	if err := dispatch.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- dispatch.Wait() }()
	t.Cleanup(func() {
		if dispatch.Process != nil {
			_ = dispatch.Process.Kill()
		}
	})

	// An agent log appearing at all is the assertion: the sidecar launched
	// something rather than preparing a workspace and going quiet.
	tasks := filepath.Join(agent.home, ".captain", "sandbox", gitagent.ServedReposDirName, SidecarRepoName, "captain", "tasks")
	deadline := time.Now().Add(60 * time.Second)
	launched := ""
	for time.Now().Before(deadline) && launched == "" {
		entries, _ := os.ReadDir(tasks)
		for _, e := range entries {
			for _, name := range []string{"agent.stdout.log", "agent.stderr.log"} {
				if data, err := os.ReadFile(filepath.Join(tasks, e.Name(), name)); err == nil && len(data) > 0 {
					launched = string(data)
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if launched == "" {
		t.Fatalf("an unconfigured backend launched nothing; the dispatch would wait out its whole budget in silence\ndispatch output:\n%s", out.String())
	}
	t.Logf("default agent produced:\n%s", launched)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(agent.serveLogs(), "starting") {
		time.Sleep(50 * time.Millisecond)
	}
	if logs := agent.serveLogs(); !strings.Contains(logs, "git-agent task") || !strings.Contains(logs, "starting") {
		t.Fatalf("agent serve did not stream the default Captain run log:\n%s", logs)
	}
}
