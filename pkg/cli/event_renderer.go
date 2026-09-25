package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/clicky"
	"github.com/segmentio/encoding/json"
	"golang.org/x/term"
)

type EventRenderer struct {
	output      io.Writer
	interactive bool
	// width is the output's own terminal width, not the process's stdout. A run
	// streaming to stderr while stdout is redirected to a file is the normal
	// case for a long command, and sizing to the wrong one of the two is how a
	// wide terminal ends up showing a line cut for an 80-column guess.
	width       int
	accumulator *promptEventAccumulator
	pending     *session.Message
	// rendered maps a tool message id to the last part state written for it, so
	// the call row and its later outcome are each written once. Keying on the id
	// alone dropped every outcome, because a result is re-emitted under the id of
	// the call it completes.
	rendered  map[string]string
	err       error
	iteration int
	hasIter   bool
	// progressDrawn records that the cursor is sitting on an in-place verify
	// status line, so the next thing written erases it first instead of landing
	// on top of it.
	progressDrawn bool
	// sessionShown guards the one-off header naming the session and model. A
	// fallback runtime re-announces itself, which is the case the line exists
	// for, so it is keyed by session id rather than by a bare bool.
	sessionShown map[string]bool
	// lastRedraw and pendingDirty throttle the in-place assistant line. A
	// delta-streaming backend emits hundreds of events a second, each one a full
	// transcript row render; pendingDirty records that a redraw was skipped so
	// the final state is still written when the turn ends.
	lastRedraw   time.Time
	pendingDirty bool
}

func NewEventRenderer(output *os.File) *EventRenderer {
	interactive := output != nil && term.IsTerminal(int(output.Fd()))
	renderer := newEventRenderer(output, interactive)
	if output != nil {
		if w, _, err := term.GetSize(int(output.Fd())); err == nil {
			renderer.width = w
		}
	}
	return renderer
}

// defaultRenderWidth is the fallback when the output is not a terminal — a
// pipe, a file, a CI log. It matches tools.MessagePreviewChars so a redirected
// run reads the same as it did before there was a width at all.
const defaultRenderWidth = 120

func newEventRenderer(output io.Writer, interactive bool) *EventRenderer {
	renderer := &EventRenderer{
		output:       output,
		interactive:  interactive,
		width:        defaultRenderWidth,
		rendered:     map[string]string{},
		sessionShown: map[string]bool{},
	}
	renderer.accumulator = newPromptEventAccumulator(renderer.consume, discardTaskSink{}, "", "")
	if cwd, err := os.Getwd(); err == nil {
		renderer.accumulator.cwd = cwd
	}
	return renderer
}

func (r *EventRenderer) Handle(iteration int, event ai.Event) {
	if r.hasIter && iteration != r.iteration {
		r.flushPending()
		r.accumulator.resetFrame()
		clear(r.rendered)
	}
	r.iteration, r.hasIter = iteration, true

	// An in-flight snapshot is redrawn over the last one and never committed to
	// the scrollback: a fixture runner reports every few hundred milliseconds,
	// and one line each would bury the run's own output under superseded counts.
	if event.Kind == ai.EventVerifyProgress {
		r.renderProgress(event)
		return
	}
	// A running tool's newest line, redrawn over the last one for the same
	// reason: it is superseded state, not transcript. A runtime that reports no
	// incremental tool output simply never sends one, and the line is absent.
	if event.Kind == ai.EventToolProgress {
		r.renderToolProgress(event)
		return
	}
	r.clearProgress()

	// A verdict is rendered here rather than through the transcript row the
	// accumulator would build, because a row is a one-line preview cut to a
	// fixed budget: it would elide the very output the verdict exists to show.
	// This is the only consumer that knows the width it is writing into, so it
	// is the only one that can fit the report instead of guessing.
	if event.Kind == ai.EventVerified || event.Kind == ai.EventVerifyFailed {
		r.flushPending()
		r.renderVerdict(event)
		return
	}

	// Which runtime is actually answering is otherwise invisible here: the
	// accumulator reports it to a task, and this renderer has no task. A run that
	// fell back from its primary model to another one looked identical to one
	// that did not, so the session line is written here instead.
	if event.Kind == ai.EventSystem && event.SessionID != "" && !r.sessionShown[event.SessionID] {
		r.sessionShown[event.SessionID] = true
		r.flushPending()
		r.renderSessionHeader(event)
	}

	if r.pendingBoundary(event.Kind) {
		r.flushPending()
	}
	r.accumulator.handle(iteration, event)
	if event.Kind == ai.EventError || event.Kind == ai.EventResult {
		r.flushPending()
	}
}

func (r *EventRenderer) Flush() error {
	r.clearProgress()
	r.flushPending()
	return r.err
}

// renderProgress redraws the verify status line in place. Only on a terminal: a
// file or a CI log cannot redraw, so writing there would produce exactly the
// line-per-snapshot spam the in-place line exists to avoid, and a run's log
// would end up mostly counts.
func (r *EventRenderer) renderProgress(event ai.Event) {
	report, ok := event.Raw.(*api.VerifyReport)
	if !ok || report == nil || !r.interactive {
		return
	}
	r.flushPending()
	line := clicky.Text("⟳ ", "text-blue-500").Append(verifyProgressStatus(*report), "text-muted").ANSI()
	r.write("\r" + ansi.EraseEntireLine + truncateANSI(line, r.width))
	r.progressDrawn = true
}

// renderToolProgress redraws a running tool's newest output line in place, on a
// terminal only — the same constraint as renderProgress, for the same reason.
func (r *EventRenderer) renderToolProgress(event ai.Event) {
	if !r.interactive || event.Text == "" {
		return
	}
	r.flushPending()
	label := event.Tool
	if label == "" {
		label = "tool"
	}
	line := clicky.Text("⟳ "+label+" ", "text-blue-500").Append(event.Text, "text-muted").ANSI()
	r.write("\r" + ansi.EraseEntireLine + truncateANSI(line, r.width))
	r.progressDrawn = true
}

// clearProgress wipes the in-place status line before anything else is written,
// so a verdict never lands on top of the counts it supersedes.
func (r *EventRenderer) clearProgress() {
	if !r.progressDrawn {
		return
	}
	r.progressDrawn = false
	r.write("\r" + ansi.EraseEntireLine)
}

func (r *EventRenderer) pendingBoundary(kind ai.EventKind) bool {
	if r.pending == nil || len(r.pending.Parts) == 0 {
		return false
	}
	pendingType := r.pending.Parts[0].Type
	switch kind {
	case ai.EventText:
		return pendingType != session.PartText
	case ai.EventThinking:
		return pendingType != session.PartReasoning
	default:
		return true
	}
}

func (r *EventRenderer) consume(message session.Message) {
	if len(message.Parts) == 0 {
		return
	}
	part := message.Parts[0]
	switch part.Type {
	case session.PartText, session.PartReasoning:
		if r.pending != nil && r.pending.ID != message.ID {
			r.flushPending()
		}
		copy := message
		r.pending = &copy
		if r.interactive {
			r.redrawPending()
		}
	case session.PartTool:
		if part.ToolName == "" {
			r.err = errors.Join(r.err, fmt.Errorf("tool result for call %q has no matching tool use", part.ToolCallID))
			return
		}
		last, seen := r.rendered[message.ID]
		if seen && last == part.State {
			return
		}
		r.rendered[message.ID] = part.State
		if !seen {
			r.renderMessage(message)
			return
		}
		r.renderToolOutcome(part)
	}
}

// renderToolOutcome writes the one line a failed tool call is worth: that it
// failed, and the head of what it said. A successful call stays silent — its
// output is the bulk of a run and the call row already said what ran — but a
// failure was invisible until the next verify verdict, which is how a fix loop
// could spend a whole turn on an edit that never applied.
//
// The merged row the accumulator re-emits cannot carry this: a transcript row
// renders a tool part from its input whenever there is one, so re-rendering it
// would reprint the call and still not show the outcome.
func (r *EventRenderer) renderToolOutcome(part session.Part) {
	if part.State != session.ToolStateOutputError && part.State != session.ToolStateOutputDenied {
		return
	}
	head := firstNonBlankLine(decodeToolOutput(part.Output))
	if head == "" {
		head = part.State
	}
	line := clicky.Text("  ↳ ✗ ", "text-red-500").Append(head, "text-muted").ANSI()
	r.write(truncateANSI(line, r.width) + "\n")
}

// renderSessionHeader names the session and the model answering in it.
func (r *EventRenderer) renderSessionHeader(event ai.Event) {
	text := "session " + event.SessionID
	if event.Model != "" {
		text += " · " + event.Model
	}
	r.write(truncateANSI(clicky.Text(text, "text-muted").ANSI(), r.width) + "\n")
}

func decodeToolOutput(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func firstNonBlankLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// redrawThrottle is the floor between two in-place redraws of the same pending
// turn. A delta-streaming runtime emits far faster than a terminal can be read,
// and every redraw is a full transcript row render.
const redrawThrottle = 50 * time.Millisecond

func (r *EventRenderer) redrawPending() {
	if r.pending == nil {
		return
	}
	if time.Since(r.lastRedraw) < redrawThrottle {
		r.pendingDirty = true
		return
	}
	r.writePending()
}

func (r *EventRenderer) writePending() {
	text, ok := transcriptMessageANSI(*r.pending)
	if !ok {
		return
	}
	r.lastRedraw, r.pendingDirty = time.Now(), false
	r.write("\r" + ansi.EraseEntireLine + text)
}

func (r *EventRenderer) flushPending() {
	if r.pending == nil {
		return
	}
	if r.interactive {
		// A throttled redraw may have left the line showing an earlier state of
		// the turn that is now finished, so the last one is always written.
		if r.pendingDirty {
			r.writePending()
		}
		r.write("\n")
	} else {
		r.renderMessage(*r.pending)
	}
	r.pending, r.pendingDirty = nil, false
}

// renderVerdict writes one verify verdict at the output's own width: the
// headline on the first line, and the verifier's output — the failure the next
// turn is about to be told about — beneath it, one line per line so a test
// runner's tables and traces keep their alignment instead of wrapping.
func (r *EventRenderer) renderVerdict(event ai.Event) {
	headline, body, _ := strings.Cut(strings.TrimRight(event.Text, "\n"), "\n")
	icon, style := "✓", "text-green-500 font-medium"
	if event.Kind == ai.EventVerifyFailed {
		icon, style = "✗", "text-red-500 font-medium"
	}
	prefix := clicky.Text(icon+" verify ", style)
	r.write(truncateANSI(prefix.Append(headline, "text-muted").ANSI(), r.width) + "\n")

	shown := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if shown == maxVerdictBodyLines {
			r.write(clicky.Text(fmt.Sprintf("  … %d more lines (full output sent to the agent)",
				countNonBlank(body)-shown), "text-muted").ANSI() + "\n")
			return
		}
		r.write(truncateANSI(line, r.width) + "\n")
		shown++
	}
}

// maxVerdictBodyLines caps how much of a verdict's output is echoed here. The
// feedback the agent receives is deliberately unbounded by this — a check's
// output streams live through the caller's verify Output sink and the whole
// tail reaches the next iteration — so a megabyte-long failure does not have to
// be replayed down the terminal a second time to be acted on.
const maxVerdictBodyLines = 200

func countNonBlank(body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func (r *EventRenderer) renderMessage(message session.Message) {
	text, ok := transcriptMessageANSI(message)
	if ok {
		r.write(text + "\n")
	}
}

func transcriptMessageANSI(message session.Message) (string, bool) {
	rows := (&session.Session{Messages: []session.Message{message}}).TranscriptRows()
	if len(rows) == 0 {
		return "", false
	}
	return rows[0].Pretty().ANSI(), true
}

func (r *EventRenderer) write(value string) {
	if r.output == nil || r.err != nil {
		return
	}
	_, err := io.WriteString(r.output, value)
	r.err = errors.Join(r.err, err)
}

type discardTaskSink struct{}

func (discardTaskSink) SetDescription(string)         {}
func (discardTaskSink) SetProgress(int, int)          {}
func (discardTaskSink) Infof(string, ...interface{})  {}
func (discardTaskSink) Warnf(string, ...interface{})  {}
func (discardTaskSink) Errorf(string, ...interface{}) {}
