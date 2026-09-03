package cli

import (
	"fmt"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/session"
	"github.com/segmentio/encoding/json"
)

// taskSink is the subset of *task.Task the accumulator drives. Injecting it as
// an interface keeps the ai.Event → session.Message mapping unit-testable
// without a live clicky task.
type taskSink interface {
	SetDescription(string)
	SetProgress(value, maximum int)
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// promptEventAccumulator converts the ai.Event stream of one prompt run into
// unified session.Message frames (the shape SessionViewer consumes) and drives
// the backing task's live status. It coalesces consecutive text/reasoning deltas
// into a single message keyed by a stable id so the viewer replaces-in-place,
// and correlates each tool call with its later result via ToolCallID.
type promptEventAccumulator struct {
	mu   sync.Mutex
	emit func(session.Message)
	// verify receives the run's verification state — every in-flight snapshot and
	// the verdict — on its own channel rather than as transcript frames. Nil for
	// a consumer with no stream behind it (the terminal renderer).
	verify func(VerifyFrame)
	// lastVerify is the newest report any verify event carried, kept so a verdict
	// that arrives without one can still close the frame without blanking it.
	lastVerify *api.VerifyReport
	task       taskSink

	sessionID string
	model     string
	backend   string
	cwd       string
	idPrefix  string

	toolByID map[string]*session.Message

	seq      int    // monotonic turn counter for text/reasoning/error ids
	textID   string // non-empty while a text turn is in flight
	textBuf  strings.Builder
	thinkID  string // non-empty while a reasoning turn is in flight
	thinkBuf strings.Builder

	tools int
	usage ai.Usage
	cost  float64
}

func newPromptEventAccumulator(emit func(session.Message), t taskSink, model, backend string) *promptEventAccumulator {
	return &promptEventAccumulator{
		emit:     emit,
		task:     t,
		model:    model,
		backend:  backend,
		toolByID: map[string]*session.Message{},
	}
}

// handle maps a single ai.Event. It matches the ai.LoopOptions.OnEvent
// signature; the iteration index is unused (prompt runs are single-iteration).
func (a *promptEventAccumulator) handle(_ int, ev ai.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ev.Model != "" {
		a.model = ev.Model
	}
	switch ev.Kind {
	case ai.EventSystem:
		if ev.SessionID != "" {
			a.sessionID = ev.SessionID
			a.task.SetDescription("starting")
			a.task.Infof("session %s", ev.SessionID)
		}
		// A lifecycle hook narrating what it did between turns (see
		// HookContext.Notify). It stands on its own rather than joining the
		// in-flight assistant turn, so the buffers are flushed first.
		if ev.Text != "" {
			a.flush()
			a.emitNotice(ev.Text)
		}
	case ai.EventThinking:
		a.appendThinking(ev.Text)
		a.task.SetDescription("thinking")
	case ai.EventText:
		a.appendText(ev.Text)
		a.task.SetDescription("responding")
	case ai.EventToolUse:
		a.flush()
		a.emitToolUse(ev)
	case ai.EventToolResult:
		a.emitToolResult(ev)
	case ai.EventPermission:
		a.task.Warnf("permission: %s awaiting approval", ev.Tool)
	case ai.EventVerifyProgress:
		// Stream state, never a transcript frame and never a log line: a check
		// reporting every few hundred milliseconds would otherwise write a
		// superseded count into the replay buffer, and into the task log, for
		// each one. The task gets its status updated in place instead.
		a.publishVerify(ev, false)
	case ai.EventVerified, ai.EventVerifyFailed:
		// The loop's definition of done, reported as it is reached. Like a
		// lifecycle notice it stands on its own rather than joining the in-flight
		// turn, so the buffers are flushed first; unlike one it carries its own
		// role, so a reader can find the verdicts in a transcript without
		// matching on prose.
		a.flush()
		a.publishVerify(ev, true)
		a.emitVerdict(ev)
	case ai.EventError:
		a.flush()
		a.emitError(ev)
	case ai.EventResult:
		a.flush()
		if len(ev.StructuredData) > 0 {
			a.appendText(string(ev.StructuredData))
			a.flush()
		}
		if ev.Usage != nil {
			a.usage = *ev.Usage
		}
		a.cost = ev.CostUSD
		a.task.SetDescription("done")
	}
}

// provenance is the transcript metadata carried on each live message.
func (a *promptEventAccumulator) provenance() *session.Provenance {
	return &session.Provenance{SessionID: a.sessionID, CWD: a.cwd, Model: a.model, Source: a.backend}
}

func (a *promptEventAccumulator) appendText(delta string) {
	if delta == "" {
		return
	}
	if a.thinkID != "" {
		a.thinkID = ""
		a.thinkBuf.Reset()
	}
	if a.textID == "" {
		a.textID = a.nextID("text")
	}
	a.textBuf.WriteString(delta)
	a.emit(session.Message{
		ID:         a.textID,
		Role:       "assistant",
		Parts:      []session.Part{{Type: session.PartText, Text: a.textBuf.String()}},
		Provenance: a.provenance(),
	})
}

func (a *promptEventAccumulator) appendThinking(delta string) {
	if delta == "" {
		return
	}
	if a.textID != "" {
		a.textID = ""
		a.textBuf.Reset()
	}
	if a.thinkID == "" {
		a.thinkID = a.nextID("think")
	}
	a.thinkBuf.WriteString(delta)
	a.emit(session.Message{
		ID:         a.thinkID,
		Role:       "assistant",
		Parts:      []session.Part{{Type: session.PartReasoning, Text: a.thinkBuf.String()}},
		Provenance: a.provenance(),
	})
}

// flush finalizes any in-flight text/reasoning turn so the next one starts a
// fresh id.
func (a *promptEventAccumulator) flush() {
	a.textID = ""
	a.textBuf.Reset()
	a.thinkID = ""
	a.thinkBuf.Reset()
}

func (a *promptEventAccumulator) resetFrame() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.flush()
	clear(a.toolByID)
}

func (a *promptEventAccumulator) emitToolUse(ev ai.Event) {
	a.tools++
	input := ev.Input
	if ev.Tool == "Bash" {
		input = bash.TransformBashInput(input)
	}
	msg := &session.Message{
		ID:   a.toolID(ev.ToolCallID),
		Role: "assistant",
		Parts: []session.Part{{
			Type:       session.PartTool,
			ToolName:   ev.Tool,
			ToolCallID: ev.ToolCallID,
			State:      session.ToolStateInputAvailable,
			Input:      mapToRaw(input),
		}},
		Provenance: a.provenance(),
	}
	if ev.ToolCallID != "" {
		a.toolByID[ev.ToolCallID] = msg
	}
	a.task.SetProgress(a.tools, 0)
	a.task.SetDescription("tool: " + ev.Tool)
	a.task.Infof("tool %s", ev.Tool)
	a.emit(*msg)
}

func (a *promptEventAccumulator) emitToolResult(ev ai.Event) {
	msg := a.toolByID[ev.ToolCallID]
	if msg == nil {
		msg = &session.Message{
			ID:         a.toolID(ev.ToolCallID),
			Role:       "assistant",
			Parts:      []session.Part{{Type: session.PartTool, ToolCallID: ev.ToolCallID}},
			Provenance: a.provenance(),
		}
		if ev.ToolCallID != "" {
			a.toolByID[ev.ToolCallID] = msg
		}
	}
	text, state := ev.Text, session.ToolStateOutputAvailable
	if !ev.Success {
		a.task.Warnf("tool %s failed", msg.Parts[0].ToolName)
		text = "[error] " + ev.Text
		state = session.ToolStateOutputError
	}
	// Re-emit the same id so the viewer merges the output into the tool row.
	msg.Parts[0].Output = textToRaw(text)
	msg.Parts[0].State = state
	a.emit(*msg)
}

func (a *promptEventAccumulator) emitError(ev ai.Event) {
	a.task.Errorf("%s", ev.Error)
	a.emit(session.Message{
		ID:         a.nextID("error"),
		Role:       "assistant",
		Parts:      []session.Part{{Type: session.PartText, Text: ev.Error}},
		Provenance: a.provenance(),
	})
}

// emitNotice renders one lifecycle line as a discrete system message. Discrete
// rather than appended to a buffer like assistant text: each notice is already
// whole when it arrives, and giving it its own id keeps a later turn's text from
// overwriting it in a viewer that dedupes by id.
func (a *promptEventAccumulator) emitNotice(text string) {
	a.task.Infof("%s", text)
	a.emit(session.Message{
		ID:         a.nextID("notice"),
		Role:       "system",
		Parts:      []session.Part{{Type: session.PartText, Text: text}},
		Provenance: a.provenance(),
	})
}

// publishVerify forwards the event's typed report as the run's current
// verification state.
//
// A progress event without a report publishes nothing: a frame with a nil report
// would blank whatever the last real snapshot put on screen. A verdict without
// one still publishes — Done is the only thing that turns the verification panel
// from a running check into a result, and withholding it left the panel spinning
// on a superseded snapshot for the rest of the run. It carries the last snapshot
// forward rather than a nil report, so the panel keeps what it was showing and
// merely stops.
func (a *promptEventAccumulator) publishVerify(ev ai.Event, done bool) {
	report, _ := ev.Raw.(*api.VerifyReport)
	if report != nil {
		a.lastVerify = report
	} else if !done {
		return
	}
	if !done && report != nil {
		a.task.SetDescription(verifyProgressStatus(*report))
	}
	if a.verify != nil {
		a.verify(VerifyFrame{Report: a.lastVerify, Done: done})
	}
}

// verifyProgressStatus is the one-line count a task's status shows while a check
// runs: how far it has got, and whether anything has gone red yet.
func verifyProgressStatus(report api.VerifyReport) string {
	s := report.Summary
	name := report.Name
	if s.Total == 0 {
		return "verifying " + name
	}
	// A producer that reports more outstanding rows than its tree has — a suite
	// still settling its own totals — must not read as negative progress in the
	// one line a person is watching.
	done := max(s.Total-s.Pending-s.Running, 0)
	if failed := s.Failed + s.TimedOut; failed > 0 {
		return fmt.Sprintf("verifying %s %d/%d, %d failed", name, done, s.Total, failed)
	}
	return fmt.Sprintf("verifying %s %d/%d", name, done, s.Total)
}

// emitVerdict records one verify verdict as its own transcript role, so it is
// selectable in a stored session rather than being one more system line. The
// typed report rides beside the prose as a data part: the verdict is a tree a
// viewer draws, and the sentence is only its headline.
func (a *promptEventAccumulator) emitVerdict(ev ai.Event) {
	role := session.RoleVerifyFailed
	if ev.Kind == ai.EventVerified {
		role = session.RoleVerified
		a.task.Infof("verified: %s", ev.Text)
	} else {
		a.task.Warnf("not verified: %s", ev.Text)
	}
	parts := []session.Part{{Type: session.PartText, Text: ev.Text}}
	if report, ok := ev.Raw.(*api.VerifyReport); ok && report != nil {
		encoded, err := json.Marshal(report)
		if err != nil {
			// The verdict itself still lands; losing the tree is a defect worth
			// naming rather than a frame worth dropping.
			a.task.Warnf("verify report for %s could not be encoded: %v", report.Name, err)
		} else {
			parts = append(parts, session.Part{Type: session.PartVerify, Data: encoded})
		}
	}
	a.emit(session.Message{
		ID:         a.nextID(string(ev.Kind)),
		Role:       role,
		Parts:      parts,
		Provenance: a.provenance(),
	})
}

func (a *promptEventAccumulator) nextID(kind string) string {
	a.seq++
	if a.idPrefix == "" {
		return fmt.Sprintf("%s-%d", kind, a.seq)
	}
	return fmt.Sprintf("%s-%s-%d", a.idPrefix, kind, a.seq)
}

func (a *promptEventAccumulator) snapshot() (string, string, ai.Usage, float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessionID, a.model, a.usage, a.cost
}

// toolID keys a tool message by its ToolCallID so the call and its later result
// share an identity. Falls back to a monotonic id when the backend omits one.
func (a *promptEventAccumulator) toolID(id string) string {
	if id != "" {
		return id
	}
	return a.nextID("tool")
}

// mapToRaw encodes a tool input map as a Part's Input JSON; empty → nil.
func mapToRaw(m map[string]any) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	if b, err := json.Marshal(m); err == nil {
		return b
	}
	return nil
}

// textToRaw encodes tool output text as a JSON string, the form the viewer
// renders directly.
func textToRaw(s string) json.RawMessage {
	if b, err := json.Marshal(s); err == nil {
		return b
	}
	return nil
}
