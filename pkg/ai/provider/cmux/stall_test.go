package cmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

// stallRunner is a thread-safe cmux Runner for the watchdog tests: it serves a
// (possibly per-call changing) read-screen and counts the Enter/Escape keys the
// watchdog and approval handler send.
type stallRunner struct {
	mu      sync.Mutex
	screen  string
	dynamic bool
	frame   int
	enters  int
	escapes int
}

func (r *stallRunner) run(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch args[0] {
	case "read-screen":
		if r.dynamic {
			r.frame++
			return fmt.Sprintf("frame %d", r.frame), nil
		}
		return r.screen, nil
	case "send-key":
		switch args[len(args)-1] {
		case "Enter":
			r.enters++
		case "Escape":
			r.escapes++
		}
	}
	return "ok", nil
}

func (r *stallRunner) setScreen(s string) {
	r.mu.Lock()
	r.screen = s
	r.mu.Unlock()
}

func (r *stallRunner) enterCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enters
}

func (r *stallRunner) escapeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.escapes
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newWatchdog(r *run, sessionID, logPath string, acc *SessionAccumulator) *stallWatchdog {
	return &stallWatchdog{
		r:         r,
		ref:       testSurface,
		sessionID: sessionID,
		logPath:   logPath,
		acc:       acc,
		timeout:   20 * time.Millisecond,
		maxNudges: 2,
		poll:      5 * time.Millisecond,
	}
}

func TestApprovalPromptRe(t *testing.T) {
	cases := []struct {
		screen string
		want   bool
	}{
		{"Do you want to proceed?", true},
		{"│ Do you want to make this edit to foo.go? │", true},
		{"❯ 1. Yes\n  2. No", false},
		{"Running tests…", false},
		{"claude is working", false},
	}
	for _, tc := range cases {
		if got := approvalPromptRe.MatchString(tc.screen); got != tc.want {
			t.Errorf("approvalPromptRe.MatchString(%q) = %v, want %v", tc.screen, got, tc.want)
		}
	}
}

func TestStallWatchdogLogGrowthResetsTimer(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, logPath, "seed")
	runner := &stallRunner{}
	runner.setScreen("claude working, no surface change")
	r := newTestRun(runConfig{}, runner.run)
	wd := newWatchdog(r, "grow", logPath, nil)

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() {
		defer func() { _ = f.Close() }()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = f.WriteString("x\n") // grows the jsonl faster than the stall timeout
			time.Sleep(3 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() { result <- wd.watch(ctx) }()

	time.Sleep(80 * time.Millisecond) // several stall windows
	close(stop)
	cancel()
	if gaveUp := <-result; gaveUp {
		t.Fatal("watchdog gave up despite continuous log growth")
	}
	if n := runner.enterCount(); n != 0 {
		t.Fatalf("nudges = %d, want 0 while the log keeps growing", n)
	}
}

func TestStallWatchdogSurfaceChangeResetsTimer(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, logPath, "seed")   // static log
	runner := &stallRunner{dynamic: true} // surface advances on every read
	r := newTestRun(runConfig{}, runner.run)
	wd := newWatchdog(r, "surface", logPath, nil)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() { result <- wd.watch(ctx) }()

	time.Sleep(80 * time.Millisecond)
	cancel()
	if gaveUp := <-result; gaveUp {
		t.Fatal("watchdog gave up despite a streaming surface (long tool call)")
	}
	if n := runner.enterCount(); n != 0 {
		t.Fatalf("nudges = %d, want 0 while the surface keeps streaming", n)
	}
}

func TestStallWatchdogBothStaticNudgesThenStalls(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, logPath, "seed") // static log
	runner := &stallRunner{}
	runner.setScreen("claude stuck, nothing moving") // static surface, not a dialog
	r := newTestRun(runConfig{}, runner.run)
	wd := newWatchdog(r, "stall", logPath, nil) // maxNudges = 2

	gaveUp := wd.watch(context.Background())

	if !gaveUp {
		t.Fatal("watchdog did not give up despite both signals being static")
	}
	if n := runner.enterCount(); n != 2 {
		t.Fatalf("nudges = %d, want 2 (StallNudges) before giving up", n)
	}
}

func TestStallWatchdogAwaitingHumanSuppressesStall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, logPath, "seed") // static log
	runner := &stallRunner{}
	runner.setScreen("╭────╮\n│ Do you want to proceed? │\n╰────╯") // approval dialog stays up
	// No CanUseTool broker: an interactive terminal user answers, so the dialog is
	// left up and the stall clock is held while it is present (no nudges, no give-up).
	r := newTestRun(runConfig{}, runner.run)
	wd := newWatchdog(r, "await-human", logPath, nil)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() { result <- wd.watch(ctx) }()

	time.Sleep(80 * time.Millisecond) // far longer than the stall timeout
	cancel()
	if gaveUp := <-result; gaveUp {
		t.Fatal("watchdog gave up while awaiting a human decision")
	}
	if n := runner.enterCount(); n != 0 {
		t.Fatalf("nudges = %d, want 0 while a tool-permission dialog is up", n)
	}
}

func TestStallWatchdogApprovalSurfacesAskState(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, logPath, "seed")
	runner := &stallRunner{}
	runner.setScreen("╭────╮\n│ Do you want to proceed? │\n╰────╯")
	r := newTestRun(runConfig{}, runner.run)

	// A live accumulator whose last log event left it "working"; the approval must
	// flip it to "ask" even though no new session-log line arrives.
	acc := GlobalSessionStats().Begin("ask-state", "claude", "", "", time.Now())
	acc.SetState(sessionStateWorking)
	wd := newWatchdog(r, "ask-state", logPath, acc)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() { result <- wd.watch(ctx) }()

	waitFor(t, "ask state", func() bool { return acc.state() == sessionStateAsk })
	cancel()
	<-result
	acc.Finish()
}

func TestHandleApprovalAllowSendsAccept(t *testing.T) {
	runner := &stallRunner{}
	r, events := recordingRun(runConfig{}, runner.run)
	r.canUseTool = func(_ context.Context, _ ai.PermissionRequest) (ai.PermissionDecision, error) {
		return ai.PermissionDecision{Allow: true}, nil
	}
	req := ai.PermissionRequest{SessionID: "allow", Tool: "Edit", Input: map[string]any{"prompt": "Do you want to make this edit?"}}

	r.handleApproval(context.Background(), testSurface, req)

	if runner.enterCount() != 1 {
		t.Fatalf("accept key sent %d times, want 1", runner.enterCount())
	}
	if runner.escapeCount() != 0 {
		t.Fatalf("escape sent on allow: %d", runner.escapeCount())
	}
	if !hasPermissionEvent(*events, "Edit") {
		t.Fatalf("no EventPermission emitted for the brokered tool, got %v", *events)
	}
}

func TestHandleApprovalDenySendsEscape(t *testing.T) {
	runner := &stallRunner{}
	r, events := recordingRun(runConfig{}, runner.run)
	r.canUseTool = func(_ context.Context, _ ai.PermissionRequest) (ai.PermissionDecision, error) {
		return ai.PermissionDecision{Allow: false, Message: "no"}, nil
	}
	req := ai.PermissionRequest{SessionID: "deny", Tool: "Bash", Input: map[string]any{"prompt": "Do you want to run this command?"}}

	r.handleApproval(context.Background(), testSurface, req)

	if runner.escapeCount() != 1 {
		t.Fatalf("deny key sent %d times, want 1", runner.escapeCount())
	}
	if runner.enterCount() != 0 {
		t.Fatalf("accept sent on deny: %d", runner.enterCount())
	}
	if !hasPermissionEvent(*events, "Bash") {
		t.Fatalf("no EventPermission emitted for the brokered tool, got %v", *events)
	}
}

func hasPermissionEvent(events []ai.Event, tool string) bool {
	for _, ev := range events {
		if ev.Kind == ai.EventPermission && ev.Tool == tool {
			return true
		}
	}
	return false
}

func TestAwaitWithStallWatchdogFailsOnStall(t *testing.T) {
	repo := t.TempDir()
	fakeClaudeHome(t)
	logPath := sessionLogFile(t, repo, "stall-sess")
	// A non-terminal line: the await never completes on its own, so only the
	// watchdog can end the wait.
	writeSessionLog(t, logPath, `{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"text","text":"working"}]}}`)

	runner := &stallRunner{}
	runner.setScreen("claude working, no surface change")
	r := newTestRun(runConfig{
		SessionLogPollInterval:  time.Millisecond,
		SessionLogAppearTimeout: time.Second,
		StallTimeout:            20 * time.Millisecond,
		StallNudges:             1,
		StallPollInterval:       5 * time.Millisecond,
	}, runner.run)

	_, completed, err := r.awaitWithStallWatchdog(context.Background(), testSurface, "stall-sess", repo, 2*time.Second, false, nil)
	if !errors.Is(err, errSessionStalled) {
		t.Fatalf("awaitWithStallWatchdog() err = %v, want errSessionStalled", err)
	}
	if completed {
		t.Fatal("completed = true, want false on a stall")
	}
	if n := runner.enterCount(); n != 1 {
		t.Fatalf("nudges = %d, want 1 (StallNudges) before failing", n)
	}
}

func TestAwaitWithStallWatchdogCompletesNormally(t *testing.T) {
	repo := t.TempDir()
	fakeClaudeHome(t)
	logPath := sessionLogFile(t, repo, "ok-sess")
	writeSessionLog(t, logPath, completedSessionLine)

	runner := &stallRunner{}
	runner.setScreen("claude done\n> ")
	r := newTestRun(runConfig{
		SessionLogPollInterval:  time.Millisecond,
		SessionLogAppearTimeout: time.Second,
		StallTimeout:            time.Minute, // must not interfere with a fast completion
		StallPollInterval:       5 * time.Second,
	}, runner.run)

	_, completed, err := r.awaitWithStallWatchdog(context.Background(), testSurface, "ok-sess", repo, 2*time.Second, false, nil)
	if err != nil {
		t.Fatalf("awaitWithStallWatchdog() error = %v", err)
	}
	if !completed {
		t.Fatal("completed = false, want true for a finished session")
	}
	if n := runner.enterCount(); n != 0 {
		t.Fatalf("nudges = %d, want 0 for a normal completion", n)
	}
}
