package cmux

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

const (
	// defaultStallTimeout is how long a run may make no progress — neither the
	// session jsonl nor the terminal surface advances — before the watchdog acts.
	defaultStallTimeout = 5 * time.Minute
	// defaultStallNudges is how many times the watchdog re-presses Enter to revive
	// a stalled turn before failing loudly.
	defaultStallNudges = 2
	// defaultStallPollInterval is how often the watchdog samples progress and the
	// surface. It is decoupled from StallTimeout so approval dialogs are detected
	// promptly even while the stall budget is minutes long.
	defaultStallPollInterval = 5 * time.Second
)

// errSessionStalled is returned when the stall watchdog gives up: neither the
// session log nor the terminal surface advanced for StallTimeout, and the
// configured nudges (re-pressing Enter) failed to revive the turn.
var errSessionStalled = errors.New("claude session stalled: no log or surface activity")

// approvalPromptRe matches claude's tool-permission dialog on the terminal
// surface ("Do you want to proceed?", "Do you want to make this edit to …?") —
// the signal that the turn is paused awaiting a human allow/deny.
var approvalPromptRe = regexp.MustCompile(`(?i)\bdo you want to\b`)

// planKeepPlanningRe / planAutoAcceptRe match the two option labels that co-occur
// only in claude's ExitPlanMode approval dialog ("Yes, and auto-accept edits" /
// "No, keep planning"). "keep planning" is unique to that dialog; "auto-accept
// edits" also appears on the *persistent* acceptEdits mode indicator ("⏵⏵
// auto-accept edits on"), so requiring BOTH excludes the indicator — keying on
// "auto-accept edits" alone would treat every acceptEdits-mode run as permanently
// awaiting a human and disable stall detection.
var planKeepPlanningRe = regexp.MustCompile(`(?i)\bkeep planning\b`)
var planAutoAcceptRe = regexp.MustCompile(`(?i)auto-accept edits`)

// isPlanApprovalDialog reports whether the surface shows claude's ExitPlanMode
// plan-approval dialog, which pauses a plan-mode turn awaiting the user's decision
// to proceed (approve, switching to acceptEdits) or keep planning (reject).
func isPlanApprovalDialog(screen string) bool {
	return planKeepPlanningRe.MatchString(screen) && planAutoAcceptRe.MatchString(screen)
}

// awaitWithStallWatchdog runs awaitSessionCompletion under a dual-signal stall
// watchdog. A watcher goroutine samples the session jsonl (log byte-growth and
// the accumulator's last-activity time) and the cmux surface (readScreen); if
// neither advances for StallTimeout it nudges (re-presses Enter) up to StallNudges
// times, then cancels the wait and reports errSessionStalled. The same watcher
// surfaces claude's tool-permission dialog for an allow/deny decision. It returns
// the same (logPath, completed, err) as the bare await, except err is
// errSessionStalled when the watchdog gives up.
func (r *run) awaitWithStallWatchdog(ctx context.Context, ref WorkspaceRef, sessionID, workDir string, timeout time.Duration, resume bool, acc *SessionAccumulator) (string, bool, error) {
	logPath, err := SessionLogPath(workDir, sessionID)
	if err != nil {
		return "", false, err
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	wd := &stallWatchdog{
		r:         r,
		ref:       ref,
		sessionID: sessionID,
		logPath:   logPath,
		acc:       acc,
		timeout:   r.stallTimeout(),
		maxNudges: r.stallNudges(),
		poll:      r.stallPollInterval(),
	}

	var stalled atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		if wd.watch(watchCtx) {
			stalled.Store(true)
			cancel()
		}
	}()

	path, completed, serr := r.awaitSessionCompletion(watchCtx, sessionID, workDir, timeout, resume, acc)
	cancel()
	<-done
	// Wait for any in-flight approval handler (spawned by the watchdog) to finish
	// emitting before the driver closes the event channel; the cancel above unblocks
	// a CanUseTool callback that honours ctx.
	r.approvals.Wait()

	if stalled.Load() {
		return path, false, fmt.Errorf("claude session %s made no progress for %s after %d nudge(s): %w", sessionID, wd.timeout, wd.maxNudges, errSessionStalled)
	}
	return path, completed, serr
}

// stallSignal is the sampled progress fingerprint: the accumulator's last
// activity time, the session-log size, and the surface contents. A change in any
// of the three means the run advanced.
type stallSignal struct {
	activity time.Time
	logSize  int64
	screen   string
}

func (s stallSignal) equal(o stallSignal) bool {
	return s.logSize == o.logSize && s.screen == o.screen && s.activity.Equal(o.activity)
}

type stallWatchdog struct {
	r         *run
	ref       WorkspaceRef
	sessionID string
	logPath   string
	acc       *SessionAccumulator
	timeout   time.Duration
	maxNudges int
	poll      time.Duration

	// approving guards against spawning more than one approval handler for the
	// same on-screen dialog.
	approving atomic.Bool
}

// watch monitors the session for stalls and surfaces tool-permission dialogs. It
// returns true when it has given up — neither signal advanced for StallTimeout
// and all nudges were exhausted — and false when ctx was cancelled (the run
// finished, normally or because the await returned).
func (w *stallWatchdog) watch(ctx context.Context) bool {
	last := w.fingerprint(w.r.readScreen(ctx, w.ref))
	lastProgress := time.Now()
	nudged := 0

	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(w.poll):
		}

		screen := w.r.readScreen(ctx, w.ref)
		w.maybeRequestApproval(ctx, screen)

		// While the turn is paused awaiting the user, hold the stall clock — a human
		// thinking about an approval (or an AskUserQuestion) is not a stall — and
		// surface the pause as the "ask" status, since claude renders the approval
		// prompt only on the terminal and no session-log event marks it.
		if w.awaitingHuman(screen) {
			w.markAwaitingHuman()
			lastProgress = time.Now()
			continue
		}

		fp := w.fingerprint(screen)
		if !fp.equal(last) {
			last = fp
			lastProgress = time.Now()
			continue
		}
		if time.Since(lastProgress) < w.timeout {
			continue
		}

		if nudged >= w.maxNudges {
			log.Errorf("cmux: session %s stalled for %s after %d nudge(s); failing", w.sessionID, w.timeout, nudged)
			return true
		}
		nudged++
		log.Warnf("cmux: session %s stalled for %s; nudging (re-pressing Enter) attempt %d/%d", w.sessionID, w.timeout, nudged, w.maxNudges)
		if err := w.r.client.SendKeySurface(ctx, w.ref.String(), w.ref.SurfaceID, "Enter"); err != nil {
			if ctx.Err() != nil {
				return false
			}
			log.Warnf("cmux: session %s nudge failed: %v", w.sessionID, err)
		}
		// Give the nudge time to take before measuring again.
		lastProgress = time.Now()
		last = w.fingerprint(w.r.readScreen(ctx, w.ref))
	}
}

func (w *stallWatchdog) fingerprint(screen string) stallSignal {
	sig := stallSignal{logSize: fileSize(w.logPath), screen: screen}
	if w.acc != nil {
		sig.activity = w.acc.lastActivity()
	}
	return sig
}

// awaitingHuman reports whether the turn is paused on a human: a tool-permission
// dialog on the surface, an approval already in flight, or an ask-tool state.
func (w *stallWatchdog) awaitingHuman(screen string) bool {
	if approvalPromptRe.MatchString(screen) || isPlanApprovalDialog(screen) || w.approving.Load() {
		return true
	}
	if w.acc != nil && w.acc.state() == sessionStateAsk {
		return true
	}
	return false
}

// markAwaitingHuman surfaces the paused-on-human condition as the "ask" status on
// the live session, so the dashboard and CLI show "awaiting input" instead of a
// stale "working" while the run waits on an approval. No-op for feedback turns,
// which do not feed the live accumulator.
func (w *stallWatchdog) markAwaitingHuman() {
	if w.acc != nil {
		w.acc.SetState(sessionStateAsk)
	}
}

// maybeRequestApproval surfaces a newly-detected tool-permission dialog for an
// allow/deny decision via the CanUseTool broker, spawning the (blocking) handler
// at most once per dialog. With no broker the dialog is left up for an
// interactive terminal user to answer (the stall clock is held by awaitingHuman).
func (w *stallWatchdog) maybeRequestApproval(ctx context.Context, screen string) {
	req, ok := detectApprovalRequest(w.sessionID, screen)
	if !ok {
		return
	}
	// A plan-only run must return ExitPlanMode to its caller. Accepting this
	// dialog would start implementation inside the same agent turn.
	if w.r.planMode && req.Tool == "ExitPlanMode" {
		if err := w.r.dismissPlanSurface(ctx, w.ref, w.sessionID); err != nil {
			log.Warnf("cmux: failed to dismiss plan-only approval: %v", err)
		}
		return
	}
	if w.r.canUseTool == nil {
		return
	}
	if !w.approving.CompareAndSwap(false, true) {
		return
	}
	w.r.approvals.Add(1)
	go func() {
		defer w.r.approvals.Done()
		defer w.approving.Store(false)
		w.r.handleApproval(ctx, w.ref, req)
	}()
}

// detectApprovalRequest recognises either claude dialog awaiting an allow/deny and
// builds the matching PermissionRequest. The plan dialog is checked first: it is the
// more specific match and its header can also satisfy approvalPromptRe, so ordering
// keeps a plan approval labelled ExitPlanMode rather than a generic tool.
func detectApprovalRequest(sessionID, screen string) (ai.PermissionRequest, bool) {
	if isPlanApprovalDialog(screen) {
		return parsePlanApprovalRequest(sessionID), true
	}
	if approvalPromptRe.MatchString(screen) {
		return parseApprovalRequest(sessionID, screen), true
	}
	return ai.PermissionRequest{}, false
}

// handleApproval brokers a tool-permission request via the CanUseTool callback
// and applies the decision on the surface: Enter accepts the highlighted "Yes",
// Escape cancels (deny). It emits an EventPermission first so the host can observe
// what is awaiting approval, then blocks on the callback (which honours ctx).
func (r *run) handleApproval(ctx context.Context, ref WorkspaceRef, req ai.PermissionRequest) {
	log.Infof("cmux: session %s awaiting tool-permission approval: %s", req.SessionID, screenSnippet(approvalSummary(req)))
	r.emit(ai.Event{Kind: ai.EventPermission, Tool: req.Tool, Input: req.Input})
	if r.canUseTool == nil {
		return
	}
	decision, err := r.canUseTool(ctx, req)
	if err != nil {
		log.Debugf("cmux: approval for session %s not resolved: %v", req.SessionID, err)
		return
	}
	if decision.Allow {
		log.Infof("cmux: approval for session %s allowed; accepting on surface", req.SessionID)
		if err := r.client.SendKeySurface(ctx, ref.String(), ref.SurfaceID, "Enter"); err != nil {
			log.Warnf("cmux: failed to send approval accept key: %v", err)
		}
		return
	}
	log.Infof("cmux: approval for session %s denied; cancelling on surface", req.SessionID)
	if err := r.client.SendKeySurface(ctx, ref.String(), ref.SurfaceID, "Escape"); err != nil {
		log.Warnf("cmux: failed to send approval deny key: %v", err)
	}
}

// parsePlanApprovalRequest builds a PermissionRequest for claude's ExitPlanMode
// plan-approval dialog. The full plan lives in the session log (surfaced separately
// by the host), so the request carries only a human-readable summary; Tool is
// "ExitPlanMode" to match the session-log tool name and the host's ask-tool set.
func parsePlanApprovalRequest(sessionID string) ai.PermissionRequest {
	return ai.PermissionRequest{
		SessionID: sessionID,
		Tool:      "ExitPlanMode",
		Input:     map[string]any{"prompt": "Claude finished planning; approve to proceed (auto-accept edits) or deny to keep planning"},
	}
}

// parseApprovalRequest builds a PermissionRequest from the dialog text on the
// surface. The tool and input are best-effort — the cmux surface carries only
// rendered text — but enough for the host to show what is being approved.
func parseApprovalRequest(sessionID, screen string) ai.PermissionRequest {
	line := approvalPromptLine(screen)
	return ai.PermissionRequest{
		SessionID: sessionID,
		Tool:      approvalTool(line),
		Input:     map[string]any{"prompt": line},
	}
}

// approvalPromptLine extracts the "Do you want to …" line from the dialog,
// stripping box-drawing borders, or a generic fallback when it cannot be found.
func approvalPromptLine(screen string) string {
	for _, raw := range strings.Split(screen, "\n") {
		line := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "│|╮╭╯╰"))
		if approvalPromptRe.MatchString(line) {
			return line
		}
	}
	return "tool permission requested"
}

// approvalTool maps the dialog's prompt line to a coarse tool name for display.
func approvalTool(line string) string {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "make this edit"):
		return "Edit"
	case strings.Contains(l, "create"):
		return "Write"
	case strings.Contains(l, "command") || strings.Contains(l, "run"):
		return "Bash"
	default:
		return "tool"
	}
}

func approvalSummary(req ai.PermissionRequest) string {
	if p, ok := req.Input["prompt"].(string); ok && p != "" {
		return p
	}
	return req.Tool + " permission requested"
}

func (r *run) stallTimeout() time.Duration {
	if r.cfg.StallTimeout > 0 {
		return r.cfg.StallTimeout
	}
	return defaultStallTimeout
}

func (r *run) stallNudges() int {
	if r.cfg.StallNudges > 0 {
		return r.cfg.StallNudges
	}
	return defaultStallNudges
}

func (r *run) stallPollInterval() time.Duration {
	if r.cfg.StallPollInterval > 0 {
		return r.cfg.StallPollInterval
	}
	return defaultStallPollInterval
}
