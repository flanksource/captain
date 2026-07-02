package cmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

// planApprovalScreen is claude's ExitPlanMode plan-approval dialog as rendered on
// the cmux surface: a plan followed by the three-option select.
const planApprovalScreen = `╭─────────────────────────────────────╮
│ Ready to code?                       │
│ Here is Claude's plan:               │
│   1. Do the thing                    │
│ Would you like to proceed?           │
│ ❯ 1. Yes, and auto-accept edits      │
│   2. Yes, and manually approve edits │
│   3. No, keep planning               │
╰─────────────────────────────────────╯`

// acceptEditsIndicatorScreen is the *persistent* mode indicator shown after a run
// switches to acceptEdits — it contains "auto-accept edits" but is NOT a dialog, so
// it must not be treated as awaiting a human (else stall detection is disabled).
const acceptEditsIndicatorScreen = "⏵⏵ auto-accept edits on (shift+tab to cycle)\nclaude working, no surface change"

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

func TestIsPlanApprovalDialog(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"plan dialog", planApprovalScreen, true},
		{"mode indicator only", acceptEditsIndicatorScreen, false},
		{"tool edit dialog", "│ Do you want to make this edit to foo.go? │", false},
		{"header without options", "Would you like to proceed?", false},
		{"keep planning without auto-accept", "❯ 3. No, keep planning", false},
		{"empty", "", false},
		{"working", "claude is working", false},
	}
	for _, tc := range cases {
		if got := isPlanApprovalDialog(tc.screen); got != tc.want {
			t.Errorf("isPlanApprovalDialog(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDetectApprovalRequest(t *testing.T) {
	cases := []struct {
		name     string
		screen   string
		wantOK   bool
		wantTool string
	}{
		{"plan dialog", planApprovalScreen, true, "ExitPlanMode"},
		{"edit dialog", "Do you want to make this edit to foo.go?", true, "Edit"},
		// A plan dialog whose header also matches approvalPromptRe must stay labelled
		// ExitPlanMode (plan-first precedence), not the generic tool.
		{"plan dialog with do-you-want header", planApprovalScreen + "\nDo you want to proceed?", true, "ExitPlanMode"},
		{"working screen", "claude working, no surface change", false, ""},
	}
	for _, tc := range cases {
		req, ok := detectApprovalRequest("sess", tc.screen)
		if ok != tc.wantOK {
			t.Fatalf("detectApprovalRequest(%s) ok = %v, want %v", tc.name, ok, tc.wantOK)
		}
		if ok && req.Tool != tc.wantTool {
			t.Errorf("detectApprovalRequest(%s) tool = %q, want %q", tc.name, req.Tool, tc.wantTool)
		}
	}
}

func TestHandleApprovalPlanAllowSendsEnter(t *testing.T) {
	runner := &stallRunner{}
	r, events := recordingRun(runConfig{}, runner.run)
	r.canUseTool = func(_ context.Context, _ ai.PermissionRequest) (ai.PermissionDecision, error) {
		return ai.PermissionDecision{Allow: true}, nil
	}

	r.handleApproval(context.Background(), testSurface, parsePlanApprovalRequest("plan-allow"))

	if runner.enterCount() != 1 {
		t.Fatalf("approve key sent %d times, want 1 (Enter selects auto-accept edits)", runner.enterCount())
	}
	if runner.escapeCount() != 0 {
		t.Fatalf("escape sent on plan approve: %d", runner.escapeCount())
	}
	if !hasPermissionEvent(*events, "ExitPlanMode") {
		t.Fatalf("no EventPermission emitted for ExitPlanMode, got %v", *events)
	}
}

func TestHandleApprovalPlanDenyKeepsPlanning(t *testing.T) {
	runner := &stallRunner{}
	r, events := recordingRun(runConfig{}, runner.run)
	r.canUseTool = func(_ context.Context, _ ai.PermissionRequest) (ai.PermissionDecision, error) {
		return ai.PermissionDecision{Allow: false, Message: "keep planning"}, nil
	}

	r.handleApproval(context.Background(), testSurface, parsePlanApprovalRequest("plan-deny"))

	if runner.escapeCount() != 1 {
		t.Fatalf("deny key sent %d times, want 1 (Escape keeps planning)", runner.escapeCount())
	}
	if runner.enterCount() != 0 {
		t.Fatalf("enter sent on plan deny: %d", runner.enterCount())
	}
	if !hasPermissionEvent(*events, "ExitPlanMode") {
		t.Fatalf("no EventPermission emitted for ExitPlanMode, got %v", *events)
	}
}

func TestStallWatchdogPlanDialogSuppressesStall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, logPath, "seed") // static log
	runner := &stallRunner{}
	runner.setScreen(planApprovalScreen) // plan-approval dialog stays up
	// No CanUseTool broker: an interactive terminal user answers, so the dialog is
	// left up and the stall clock is held while it is present (no nudges, no give-up).
	r := newTestRun(runConfig{}, runner.run)
	wd := newWatchdog(r, "plan-await", logPath, nil)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() { result <- wd.watch(ctx) }()

	time.Sleep(80 * time.Millisecond) // far longer than the stall timeout
	cancel()
	if gaveUp := <-result; gaveUp {
		t.Fatal("watchdog gave up while a plan-approval dialog is up")
	}
	if n := runner.enterCount(); n != 0 {
		t.Fatalf("nudges = %d, want 0 while a plan-approval dialog is up", n)
	}
}

func TestStallWatchdogAcceptEditsIndicatorStillStalls(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, logPath, "seed") // static log
	runner := &stallRunner{}
	// The persistent acceptEdits indicator contains "auto-accept edits" but is not a
	// dialog: the watchdog must still detect the stall and nudge/give up, not treat
	// the indicator as awaiting a human forever.
	runner.setScreen(acceptEditsIndicatorScreen)
	r := newTestRun(runConfig{}, runner.run)
	wd := newWatchdog(r, "indicator-stall", logPath, nil) // maxNudges = 2

	gaveUp := wd.watch(context.Background())

	if !gaveUp {
		t.Fatal("watchdog did not give up on a static acceptEdits-indicator screen")
	}
	if n := runner.enterCount(); n != 2 {
		t.Fatalf("nudges = %d, want 2 (StallNudges) before giving up", n)
	}
}

func TestStallWatchdogPlanApprovalBrokeredOnce(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, logPath, "seed")

	// The runner serves the plan dialog until an Enter is sent, then an advancing
	// (non-dialog) screen — mirroring the dialog dismissing on approval and the turn
	// resuming work. A correct watchdog brokers the plan exactly once and, seeing the
	// surface advance afterwards, does not nudge, so only the approval Enter is sent.
	var mu sync.Mutex
	var enters, frame int
	runner := func(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		switch args[0] {
		case "read-screen":
			if enters == 0 {
				return planApprovalScreen, nil
			}
			frame++
			return fmt.Sprintf("claude working %d", frame), nil
		case "send-key":
			if args[len(args)-1] == "Enter" {
				enters++
			}
		}
		return "ok", nil
	}
	r := newTestRun(runConfig{}, runner)
	var broker atomic.Int32
	r.canUseTool = func(_ context.Context, _ ai.PermissionRequest) (ai.PermissionDecision, error) {
		broker.Add(1)
		return ai.PermissionDecision{Allow: true}, nil
	}
	wd := newWatchdog(r, "plan-once", logPath, nil)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() { result <- wd.watch(ctx) }()

	waitFor(t, "plan approval brokered", func() bool { return broker.Load() >= 1 })
	time.Sleep(40 * time.Millisecond) // give any erroneous re-broker time to fire
	cancel()
	<-result
	r.approvals.Wait()

	if got := broker.Load(); got != 1 {
		t.Fatalf("plan approval brokered %d times, want exactly 1", got)
	}
	mu.Lock()
	gotEnters := enters
	mu.Unlock()
	if gotEnters != 1 {
		t.Fatalf("Enter sent %d times, want 1 (approve once)", gotEnters)
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
