package cmux

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

func TestReplReadyRe(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"bare prompt", "> ", true},
		{"unicode prompt", "❯ ", true},
		{"boxed prompt", "╭────────╮\n│ >      │\n╰────────╯", true},
		{"indented prompt", "  > ", true},
		{"shell dollar", "user@host:~$ ", false},
		{"plain banner", "Welcome to Claude Code\nStarting up…", false},
		{"prose with arrow midline", "Type > to send your message", false},
	}
	for _, tc := range cases {
		if got := replReadyRe.MatchString(tc.screen); got != tc.want {
			t.Errorf("%s: replReadyRe.MatchString(%q) = %v, want %v", tc.name, tc.screen, got, tc.want)
		}
	}
}

func TestDefaultSessionStartRetryDelaysEscalate(t *testing.T) {
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}
	if len(defaultSessionStartRetryDelays) != len(want) {
		t.Fatalf("defaultSessionStartRetryDelays = %v, want %v", defaultSessionStartRetryDelays, want)
	}
	for i := range want {
		if defaultSessionStartRetryDelays[i] != want[i] {
			t.Fatalf("defaultSessionStartRetryDelays = %v, want %v", defaultSessionStartRetryDelays, want)
		}
	}
}

// timedRunner records each cmux invocation with the time it occurred so a test
// can assert the settle delay between the paste and the Enter.
type timedRunner struct {
	mu    sync.Mutex
	ops   []string
	times []time.Time
}

func (r *timedRunner) run(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
	r.mu.Lock()
	r.ops = append(r.ops, args[0])
	r.times = append(r.times, time.Now())
	r.mu.Unlock()
	return "ok", nil
}

// firstTimeOf returns the time the runner first saw op (and whether it saw it).
func (r *timedRunner) firstTimeOf(op string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, o := range r.ops {
		if o == op {
			return r.times[i], true
		}
	}
	return time.Time{}, false
}

// pasteRunner serves the pre-paste baseline until a paste (send) arrives, then a
// changed screen, so a test drives waitForPasteLanded through a real surface change.
type pasteRunner struct {
	mu       sync.Mutex
	baseline string
	after    string
	sent     bool
	ops      []string
}

func (r *pasteRunner) run(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ops = append(r.ops, args[0])
	switch args[0] {
	case "read-screen":
		if r.sent {
			return r.after, nil
		}
		return r.baseline, nil
	case "send":
		r.sent = true
	}
	return "ok", nil
}

func (r *pasteRunner) opSequence() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ops...)
}

// codexSurfaceRunner models codex's composer: the paste lands (the surface changes
// from the pre-paste baseline) but the first Enter is dropped, so the composer keeps
// showing the typed prompt until a re-press submits it and the surface advances to a
// working state (after the second Enter).
type codexSurfaceRunner struct {
	mu     sync.Mutex
	sent   bool
	enters int
}

func (r *codexSurfaceRunner) run(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch args[0] {
	case "read-screen":
		switch {
		case !r.sent:
			return "codex ready\n> ", nil
		case r.enters >= 2:
			return "codex working…\nesc to interrupt", nil
		default:
			return "codex ready\n> run the task", nil
		}
	case "send":
		r.sent = true
	case "send-key":
		if args[len(args)-1] == "Enter" {
			r.enters++
		}
	}
	return "ok", nil
}

func (r *codexSurfaceRunner) enterCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enters
}

func TestWaitForPasteLandedDetectsChangeThenQuiesce(t *testing.T) {
	// The surface differs from the pre-paste baseline and holds stable, so the paste
	// is confirmed landed within the settle/quiesce window.
	r := newTestRun(runConfig{
		PasteLandTimeout:      2 * time.Second,
		PasteLandPollInterval: 2 * time.Millisecond,
		SendSettleDelay:       20 * time.Millisecond,
	}, constantScreenRunner("idle\n> prompt text"))

	landed, err := r.waitForPasteLanded(context.Background(), testSurface, "idle")
	if err != nil {
		t.Fatalf("waitForPasteLanded() error = %v", err)
	}
	if !landed {
		t.Fatal("waitForPasteLanded() = false, want true once the surface changed and held stable")
	}
}

func TestWaitForPasteLandedTimesOutWhenNoChange(t *testing.T) {
	// The surface never changes from the baseline, so the paste is never observed
	// landing; the wait must time out so the caller falls back to the fixed settle.
	r := newTestRun(runConfig{
		PasteLandTimeout:      30 * time.Millisecond,
		PasteLandPollInterval: 2 * time.Millisecond,
		SendSettleDelay:       20 * time.Millisecond,
	}, constantScreenRunner("idle"))

	start := time.Now()
	landed, err := r.waitForPasteLanded(context.Background(), testSurface, "idle")
	if err != nil {
		t.Fatalf("waitForPasteLanded() error = %v", err)
	}
	if landed {
		t.Fatal("waitForPasteLanded() = true, want false when the surface never changed")
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("waitForPasteLanded returned after %s, want it to wait ~PasteLandTimeout (30ms)", elapsed)
	}
}

func TestSendSurfaceTextPressesEnterOncePasteLands(t *testing.T) {
	runner := &pasteRunner{baseline: "codex ready\n> ", after: "codex ready\n> hello"}
	// A large PasteLandTimeout (2s) with a small settle/quiesce (20ms): the paste
	// lands on-screen immediately, so Enter must fire ~one quiesce window later. Had
	// the change not been observed, the send would burn the full 2s timeout first.
	r := newTestRun(runConfig{
		PasteLandTimeout:      2 * time.Second,
		PasteLandPollInterval: 2 * time.Millisecond,
		SendSettleDelay:       20 * time.Millisecond,
	}, runner.run)

	start := time.Now()
	if err := r.sendSurfaceText(context.Background(), testSurface, "prompt", "hello"); err != nil {
		t.Fatalf("sendSurfaceText() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("sendSurfaceText took %s, want Enter pressed adaptively once the paste landed, not after the 2s timeout", elapsed)
	}

	ops := runner.opSequence()
	sendIdx, sendKeyIdx, polls := -1, -1, 0
	for i, op := range ops {
		switch {
		case op == "send" && sendIdx == -1:
			sendIdx = i
		case op == "send-key" && sendKeyIdx == -1:
			sendKeyIdx = i
		case op == "read-screen" && sendIdx != -1 && sendKeyIdx == -1:
			polls++
		}
	}
	if sendIdx == -1 || sendKeyIdx == -1 || sendKeyIdx < sendIdx {
		t.Fatalf("ops = %v, want a send followed by a send-key", ops)
	}
	if polls == 0 {
		t.Fatalf("ops = %v, want the send to poll read-screen for the paste to land before Enter", ops)
	}
}

func TestSendSurfaceTextFallsBackToSettleWhenPasteUnobserved(t *testing.T) {
	runner := &timedRunner{}
	// timedRunner's read-screen never changes, so the paste is never observed
	// landing and the send must fall back to the settle delay before Enter.
	r := newTestRun(runConfig{
		PasteLandTimeout:      20 * time.Millisecond,
		PasteLandPollInterval: 2 * time.Millisecond,
		SendSettleDelay:       50 * time.Millisecond,
	}, runner.run)

	if err := r.sendSurfaceText(context.Background(), testSurface, "prompt", "hello"); err != nil {
		t.Fatalf("sendSurfaceText() error = %v", err)
	}

	sendAt, ok := runner.firstTimeOf("send")
	if !ok {
		t.Fatal("no send op recorded")
	}
	sendKeyAt, ok := runner.firstTimeOf("send-key")
	if !ok {
		t.Fatal("no send-key op recorded; Enter was never pressed on the fallback path")
	}
	// Fallback path waits PasteLandTimeout (20ms) then the settle (50ms) before Enter.
	if gap := sendKeyAt.Sub(sendAt); gap < 45*time.Millisecond {
		t.Fatalf("paste→Enter gap = %s, want >= ~50ms settle fallback", gap)
	}
}

func TestSubmitAndConfirmRepressesEnterUntilSurfaceAdvances(t *testing.T) {
	runner := &codexSurfaceRunner{}
	r := newTestRun(runConfig{
		PasteLandTimeout:        time.Second,
		PasteLandPollInterval:   time.Millisecond,
		SendSettleDelay:         2 * time.Millisecond,
		SessionStartRetryDelays: []time.Duration{2 * time.Millisecond, 2 * time.Millisecond, 2 * time.Millisecond, 2 * time.Millisecond},
	}, runner.run)

	// submitConfirm{} carries no session log (the codex path), so confirmation is
	// purely the surface advancing past the just-submitted screen.
	if err := r.submitAndConfirm(context.Background(), testSurface, "initial prompt", "run the task", submitConfirm{}); err != nil {
		t.Fatalf("submitAndConfirm() error = %v", err)
	}
	if got := runner.enterCount(); got != 2 {
		t.Fatalf("Enter presses = %d, want 2 (initial send + one re-press before the surface advanced)", got)
	}
}

func TestConfirmStartedInitialDetectsLogAppearance(t *testing.T) {
	repo := t.TempDir()
	fakeClaudeHome(t)
	logPath := sessionLogFile(t, repo, "sess")
	writeSessionLog(t, logPath, completedSessionLine)

	// read-screen returns the same content as postSend, so the only signal that can
	// fire is the log having appeared.
	r := newTestRun(runConfig{}, constantScreenRunner("idle\n> "))
	started, why := r.confirmStarted(context.Background(), testSurface, submitConfirm{logPath: logPath}, "idle\n> ")
	if !started {
		t.Fatalf("confirmStarted() = false, want true once the log exists")
	}
	if why != "session log appeared" {
		t.Fatalf("confirmStarted reason = %q, want %q", why, "session log appeared")
	}
}

func TestConfirmStartedFeedbackDetectsLogGrowth(t *testing.T) {
	repo := t.TempDir()
	fakeClaudeHome(t)
	logPath := sessionLogFile(t, repo, "sess")
	writeSessionLog(t, logPath, completedSessionLine) // the prior turn
	base := fileSize(logPath)

	// read-screen matches the post-send baseline so the surface signal never fires,
	// isolating log-growth as the only confirmation signal.
	r := newTestRun(runConfig{}, constantScreenRunner("idle prompt"))
	sc := submitConfirm{logPath: logPath, baseOffset: base, growth: true}

	if started, _ := r.confirmStarted(context.Background(), testSurface, sc, "idle prompt"); started {
		t.Fatal("confirmStarted() = true before the log grew, want false")
	}

	appendSessionLine(t, logPath, `{"type":"user","message":{"content":[{"type":"text","text":"feedback"}]}}`)
	started, why := r.confirmStarted(context.Background(), testSurface, sc, "idle prompt")
	if !started {
		t.Fatal("confirmStarted() = false after the log grew, want true")
	}
	if why != "session log grew past pre-send offset" {
		t.Fatalf("confirmStarted reason = %q, want growth reason", why)
	}
}

func TestConfirmStartedDetectsSurfaceAdvance(t *testing.T) {
	// No log on disk and growth disabled, so only a surface change can confirm.
	r := newTestRun(runConfig{}, constantScreenRunner("claude is working…"))
	sc := submitConfirm{logPath: "/nonexistent/session.jsonl"}

	started, why := r.confirmStarted(context.Background(), testSurface, sc, "prompt waiting to submit")
	if !started {
		t.Fatal("confirmStarted() = false, want true when the surface advanced past submission")
	}
	if why != "surface advanced past submission" {
		t.Fatalf("confirmStarted reason = %q, want surface reason", why)
	}
}

func TestWaitForREPLReadyDetectsPrompt(t *testing.T) {
	r := newTestRun(runConfig{ScreenPollInterval: time.Millisecond}, constantScreenRunner("Claude ready\n> "))
	got, err := r.waitForREPLReady(context.Background(), testSurface, 2*time.Second, "shell ready\n$ ")
	if err != nil {
		t.Fatalf("waitForREPLReady() error = %v", err)
	}
	if got == "" || !replReadyRe.MatchString(got) {
		t.Fatalf("waitForREPLReady() = %q, want the ready screen matching the prompt", got)
	}
}

func TestWaitForREPLReadyFallsBackOnTimeout(t *testing.T) {
	// The surface never renders a recognizable prompt; readiness must fall back to
	// the screen-idle wait (a changed, stable screen) rather than failing.
	r := newTestRun(runConfig{
		REPLReadyTimeout:     10 * time.Millisecond,
		ScreenPollInterval:   time.Millisecond,
		ScreenStableDuration: time.Millisecond,
	}, constantScreenRunner("compiling, no prompt yet"))
	got, err := r.waitForREPLReady(context.Background(), testSurface, time.Second, "shell ready\n$ ")
	if err != nil {
		t.Fatalf("waitForREPLReady() fallback error = %v", err)
	}
	if got != "compiling, no prompt yet" {
		t.Fatalf("waitForREPLReady() fallback = %q, want the idle screen", got)
	}
}

// constantScreenRunner is a cmux Runner whose read-screen always returns screen
// and whose other commands succeed, for tests that drive surface logic directly.
func constantScreenRunner(screen string) Runner {
	return func(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
		if args[0] == "read-screen" {
			return screen, nil
		}
		return "ok", nil
	}
}

// appendSessionLine appends one line to an existing session log on disk.
func appendSessionLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}
