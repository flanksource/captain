package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/x/ansi"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/session"
	"golang.org/x/term"
)

type EventRenderer struct {
	output      io.Writer
	interactive bool
	accumulator *promptEventAccumulator
	pending     *session.Message
	rendered    map[string]bool
	err         error
	iteration   int
	hasIter     bool
}

func NewEventRenderer(output *os.File) *EventRenderer {
	return newEventRenderer(output, output != nil && term.IsTerminal(int(output.Fd())))
}

func newEventRenderer(output io.Writer, interactive bool) *EventRenderer {
	renderer := &EventRenderer{
		output:      output,
		interactive: interactive,
		rendered:    map[string]bool{},
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

	if r.pendingBoundary(event.Kind) {
		r.flushPending()
	}
	r.accumulator.handle(iteration, event)
	if event.Kind == ai.EventError || event.Kind == ai.EventResult {
		r.flushPending()
	}
}

func (r *EventRenderer) Flush() error {
	r.flushPending()
	return r.err
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
		if r.rendered[message.ID] {
			return
		}
		r.rendered[message.ID] = true
		r.renderMessage(message)
	}
}

func (r *EventRenderer) redrawPending() {
	if r.pending == nil {
		return
	}
	text, ok := transcriptMessageANSI(*r.pending)
	if !ok {
		return
	}
	r.write("\r" + ansi.EraseEntireLine + text)
}

func (r *EventRenderer) flushPending() {
	if r.pending == nil {
		return
	}
	if r.interactive {
		r.write("\n")
	} else {
		r.renderMessage(*r.pending)
	}
	r.pending = nil
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
