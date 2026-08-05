package cli

import (
	"bytes"
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
	t    *testing.T
	home string
	bin  string
}

func newHost(t *testing.T) *host {
	t.Helper()
	return &host{t: t, home: t.TempDir(), bin: captainBinary(t)}
}

func (h *host) env() []string {
	return append(os.Environ(), "HOME="+h.home, "CAPTAIN_SESSION_DB_URL=off")
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
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
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
		case err := <-done:
			h.t.Fatalf("serve exited before listening (%v):\n%s", err, out.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	h.t.Fatalf("serve never began listening:\n%s", out.String())
}

func (h *host) configBytes() string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.home, ".captain.yaml"))
	if err != nil {
		h.t.Fatalf("reading %s: %v", filepath.Join(h.home, ".captain.yaml"), err)
	}
	return string(data)
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
	repo = newRepo(t)

	supPort := freeLocalPort(t)
	supervisor.serve(supPort, "--role", "mailbox", "--repo", repo)

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
	for _, required := range []string{"--join", "--supervisor", "--host-fingerprint"} {
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

	// The agent must have authorized the supervisor's dispatch key, and
	// recorded a relay URL carrying a repository path.
	agentCfg := agent.configBytes()
	if !strings.Contains(agentCfg, add.DispatchKey) {
		t.Fatalf("the agent did not authorize the supervisor's dispatch key %s:\n%s", add.DispatchKey, agentCfg)
	}
	if !strings.Contains(agentCfg, MailboxRepoName) {
		t.Fatalf("the agent's relay URL carries no repository path:\n%s", agentCfg)
	}

	// The supervisor must serve a mailbox, at the path the relay names, with
	// mailbox-role hooks and objects shared with the real repository.
	mailbox := filepath.Join(supervisor.home, ".captain", "sandbox", servedReposDir, MailboxRepoName)
	if _, err := os.Stat(filepath.Join(mailbox, "HEAD")); err != nil {
		t.Fatalf("no mailbox repository where the relay points: %v", err)
	}
	shim, err := os.ReadFile(filepath.Join(mailbox, "hooks", "pre-receive"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shim), "mailbox") {
		t.Fatalf("mailbox hooks run with the wrong role:\n%s", shim)
	}
	alternates, err := os.ReadFile(filepath.Join(mailbox, "objects", "info", "alternates"))
	if err != nil {
		t.Fatalf("the mailbox does not share the repository's objects: %v", err)
	}
	if !strings.Contains(string(alternates), repo) {
		t.Fatalf("mailbox alternates %q do not point at %s", alternates, repo)
	}
}

// TestFullCycleThroughTheCLI is the §12 baseline check at product level: with
// nothing but the documented commands, a dispatch reaches the agent, the agent
// completes it with `git commit` and a bare `git push`, the result relays to
// the supervisor, and accepted work is integrated. Every setup blocker this
// suite exists for shows up here as a hang or a missing ref.
func TestFullCycleThroughTheCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary and runs two endpoints")
	}
	supervisor, agent, repo, _, _ := enrollPair(t)

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
	tasksDir := filepath.Join(agent.home, ".captain", "sandbox", servedReposDir, SidecarRepoName, "captain", "tasks")
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
	mailbox := filepath.Join(supervisor.home, ".captain", "sandbox", servedReposDir, MailboxRepoName)
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

// TestJoinTokenIsSingleUse pins that the bidirectional exchange keeps the
// single-use property (R8.2).
func TestJoinTokenIsSingleUse(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the captain binary")
	}
	_, _, _, _, add := enrollPair(t)

	replay := newHost(t)
	args := append([]string{"sandbox", "git-agent", "serve", "--listen", "127.0.0.1:" + freeLocalPort(t)},
		parseJoin(t, add.JoinCommand)...)
	out, err := replay.run(args...)
	if err == nil {
		t.Fatalf("token replay must be refused:\n%s", out)
	}
	if !strings.Contains(out, "already used") {
		t.Fatalf("replay refusal must name the cause:\n%s", out)
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
