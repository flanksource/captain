package load

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai/assistanttags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
)

const (
	structuredOutputTool = "StructuredOutput"
	askUserQuestionTool  = "AskUserQuestion"
)

// DeriveAwaitingInput sets the questions the session's last turn left
// unanswered, walking back from the newest message to the last thing that
// either asked or ended the run.
//
// Whatever comes after that anchor decides whether it still waits: a user
// message with text is an answer, and a conversational assistant message means
// the agent carried on. A tool result the SDK writes back and the title a run
// names itself with are neither. A native AskUserQuestion that is awaiting
// approval belongs to the approval flow. Whether the agent's process is still
// running is not known here; the caller that joins liveness clears it.
func DeriveAwaitingInput(aggregate *session.Session) error {
	aggregate.AwaitingInput = nil
	stored, err := newStoredEnvelope(aggregate.StructuredOutput)
	if err != nil {
		return err
	}
	for i := len(aggregate.Messages) - 1; i >= 0; i-- {
		message := aggregate.Messages[i]
		ask, anchored, err := askAnchor(message, stored)
		if err != nil {
			return fmt.Errorf("message %s: %w", message.ID, err)
		}
		if anchored {
			if ask != nil && (ask.Origin != session.AwaitingInputAskUserQuestion || aggregate.Approvals.Pending == 0) {
				aggregate.AwaitingInput = ask
			}
			return nil
		}
		if answersAsk(message) {
			return nil
		}
	}
	return nil
}

// askAnchor reports whether message is where the run asked or ended, and the
// ask it left when that ending was a question.
func askAnchor(message session.Message, stored storedEnvelope) (*session.AwaitingInput, bool, error) {
	if message.Role != "assistant" {
		return nil, false, nil
	}
	for i := len(message.Parts) - 1; i >= 0; i-- {
		part := message.Parts[i]
		switch {
		case part.ToolName == structuredOutputTool:
			envelope, ok, err := assistanttags.ParseEnvelope(string(part.Input))
			if ok || err != nil {
				return envelopeAsk(envelope, message.ID, part.ToolCallID), true, err
			}
		case part.ToolName == askUserQuestionTool && part.State == session.ToolStateOutputDenied:
			// The user refused to answer: that ended the question.
			return nil, true, nil
		case part.ToolName == askUserQuestionTool && part.State != session.ToolStateOutputAvailable:
			ask, err := askUserQuestion(part, message.ID)
			return ask, true, err
		case part.Type == session.PartText:
			envelope, ok, err := assistanttags.ParseEnvelope(part.Text)
			if ok || err != nil {
				return envelopeAsk(envelope, message.ID, ""), true, err
			}
			if stored.summary != "" && strings.TrimSpace(part.Text) == stored.summary {
				envelope, _, err := assistanttags.ParseEnvelope(stored.raw)
				return envelopeAsk(envelope, message.ID, ""), true, err
			}
		}
	}
	return nil, false, nil
}

func envelopeAsk(envelope assistanttags.Envelope, messageID, toolCallID string) *session.AwaitingInput {
	if envelope.EndStatus != "ask" || len(envelope.Questions) == 0 {
		return nil
	}
	return &session.AwaitingInput{
		Origin: session.AwaitingInputEnvelope, Summary: envelope.Summary, Questions: envelope.Questions,
		MessageID: messageID, ToolCallID: toolCallID,
	}
}

func askUserQuestion(part session.Part, messageID string) (*session.AwaitingInput, error) {
	var input map[string]any
	if err := json.Unmarshal(part.Input, &input); err != nil {
		return nil, fmt.Errorf("decode %s input: %w", askUserQuestionTool, err)
	}
	questions, err := api.TerminalQuestionsFromInput(input)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", askUserQuestionTool, err)
	}
	return &session.AwaitingInput{
		Origin: session.AwaitingInputAskUserQuestion, Questions: questions,
		MessageID: messageID, ToolCallID: part.ToolCallID,
	}, nil
}

// storedEnvelope is the envelope a prompt run recorded, which is the only place
// its questions survive when the transcript rendered it as its summary. Only its
// summary is read up front; its questions are parsed once a message matches it,
// so an envelope a later turn already answered never fails a read.
type storedEnvelope struct {
	raw, summary string
}

func newStoredEnvelope(output map[string]any) (storedEnvelope, error) {
	if len(output) == 0 {
		return storedEnvelope{}, nil
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return storedEnvelope{}, fmt.Errorf("encode structured output: %w", err)
	}
	summary, ok := assistanttags.EnvelopeSummary(string(raw))
	if !ok {
		return storedEnvelope{}, nil
	}
	return storedEnvelope{raw: string(raw), summary: summary}, nil
}

func answersAsk(message session.Message) bool {
	switch message.Role {
	case "user":
		for _, part := range message.Parts {
			if part.Type == session.PartText && strings.TrimSpace(part.Text) != "" {
				return true
			}
		}
		return false
	case "assistant":
		return session.IsConversationalMessage(message)
	default:
		return false
	}
}
