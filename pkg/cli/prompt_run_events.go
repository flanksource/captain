package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
)

// taskSink is the subset of *task.Task the accumulator drives. Injecting it as
// an interface keeps the ai.Event → SessionEntryWire mapping unit-testable
// without a live clicky task.
type taskSink interface {
	SetDescription(string)
	SetProgress(value, maximum int)
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// promptEventAccumulator converts the ai.Event stream of one prompt run into
// SessionEntryWire frames (the shape SessionViewer consumes) and drives the
// backing task's live status. It coalesces consecutive text/thinking deltas
// into a single entry keyed by a stable UUID so the viewer replaces-in-place,
// and correlates each tool call with its later result via ToolCallID.
type promptEventAccumulator struct {
	emit func(SessionEntryWire)
	task taskSink

	sessionID string
	model     string
	backend   string
	cwd       string

	toolByID map[string]*SessionToolUseWire

	seq       int    // monotonic turn counter for text/thinking/error UUIDs
	textUUID  string // non-empty while a text turn is in flight
	textBuf   strings.Builder
	thinkUUID string // non-empty while a thinking turn is in flight
	thinkBuf  strings.Builder

	tools int
	usage ai.Usage
	cost  float64
}

func newPromptEventAccumulator(emit func(SessionEntryWire), t taskSink, model, backend string) *promptEventAccumulator {
	return &promptEventAccumulator{
		emit:     emit,
		task:     t,
		model:    model,
		backend:  backend,
		toolByID: map[string]*SessionToolUseWire{},
	}
}

// handle maps a single ai.Event. It matches the ai.LoopOptions.OnEvent
// signature; the iteration index is unused (prompt runs are single-iteration).
func (a *promptEventAccumulator) handle(_ int, ev ai.Event) {
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
		if ev.Usage != nil {
			a.usage = *ev.Usage
		}
		a.cost = ev.CostUSD
		a.task.SetDescription("done")
	}
}

func (a *promptEventAccumulator) appendText(delta string) {
	if delta == "" {
		return
	}
	if a.thinkUUID != "" {
		a.thinkUUID = ""
		a.thinkBuf.Reset()
	}
	if a.textUUID == "" {
		a.textUUID = a.nextUUID("text")
	}
	a.textBuf.WriteString(delta)
	a.emit(SessionEntryWire{
		Type:      "assistant",
		Message:   &SessionMessageWire{Role: "assistant", Content: []SessionContentWire{{Type: "text", Text: a.textBuf.String()}}},
		SessionID: a.sessionID,
		CWD:       a.cwd,
		UUID:      a.textUUID,
	})
}

func (a *promptEventAccumulator) appendThinking(delta string) {
	if delta == "" {
		return
	}
	if a.textUUID != "" {
		a.textUUID = ""
		a.textBuf.Reset()
	}
	if a.thinkUUID == "" {
		a.thinkUUID = a.nextUUID("think")
	}
	a.thinkBuf.WriteString(delta)
	a.emit(SessionEntryWire{
		Type:      "assistant",
		Message:   &SessionMessageWire{Role: "assistant", Content: []SessionContentWire{{Type: "thinking", Thinking: a.thinkBuf.String()}}},
		SessionID: a.sessionID,
		CWD:       a.cwd,
		UUID:      a.thinkUUID,
	})
}

// flush finalizes any in-flight text/thinking turn so the next one starts a
// fresh UUID.
func (a *promptEventAccumulator) flush() {
	a.textUUID = ""
	a.textBuf.Reset()
	a.thinkUUID = ""
	a.thinkBuf.Reset()
}

func (a *promptEventAccumulator) emitToolUse(ev ai.Event) {
	a.tools++
	tu := &SessionToolUseWire{
		Tool:      ev.Tool,
		Input:     ev.Input,
		SessionID: a.sessionID,
		CWD:       a.cwd,
		ToolUseID: ev.ToolCallID,
		Model:     a.model,
		Source:    a.backend,
	}
	if ev.ToolCallID != "" {
		a.toolByID[ev.ToolCallID] = tu
	}
	a.task.SetProgress(a.tools, 0)
	a.task.SetDescription("tool: " + ev.Tool)
	a.task.Infof("tool %s", ev.Tool)
	a.emit(SessionEntryWire{
		Type:      "assistant",
		ToolUse:   tu,
		SessionID: a.sessionID,
		CWD:       a.cwd,
		UUID:      a.toolUUID(ev.ToolCallID),
	})
}

func (a *promptEventAccumulator) emitToolResult(ev ai.Event) {
	tu := a.toolByID[ev.ToolCallID]
	if tu == nil {
		tu = &SessionToolUseWire{ToolUseID: ev.ToolCallID, SessionID: a.sessionID, CWD: a.cwd, Source: a.backend}
		if ev.ToolCallID != "" {
			a.toolByID[ev.ToolCallID] = tu
		}
	}
	tu.Response = ev.Text
	if !ev.Success {
		a.task.Warnf("tool %s failed", tu.Tool)
		tu.Response = "[error] " + ev.Text
	}
	// Re-emit the same UUID so the viewer merges the response into the tool row.
	a.emit(SessionEntryWire{
		Type:      "assistant",
		ToolUse:   tu,
		SessionID: a.sessionID,
		CWD:       a.cwd,
		UUID:      a.toolUUID(ev.ToolCallID),
	})
}

func (a *promptEventAccumulator) emitError(ev ai.Event) {
	a.task.Errorf("%s", ev.Error)
	a.emit(SessionEntryWire{
		Type:              "assistant",
		IsAPIErrorMessage: true,
		Error:             ev.Error,
		Message:           &SessionMessageWire{Role: "assistant", Content: []SessionContentWire{{Type: "text", Text: ev.Error}}},
		SessionID:         a.sessionID,
		CWD:               a.cwd,
		UUID:              a.nextUUID("error"),
	})
}

func (a *promptEventAccumulator) nextUUID(kind string) string {
	a.seq++
	return fmt.Sprintf("%s-%d", kind, a.seq)
}

// toolUUID keys a tool row by its ToolCallID so the call and its later result
// share an identity. Falls back to a monotonic id when the backend omits one.
func (a *promptEventAccumulator) toolUUID(id string) string {
	if id != "" {
		return id
	}
	return a.nextUUID("tool")
}
