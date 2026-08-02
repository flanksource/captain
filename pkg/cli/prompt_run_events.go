package cli

import (
	"fmt"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
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
	task taskSink

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
		}
		a.task.SetDescription("starting")
		if ev.SessionID != "" {
			a.task.Infof("session %s", ev.SessionID)
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
