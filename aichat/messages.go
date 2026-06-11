package aichat

import (
	"fmt"

	"github.com/firebase/genkit/go/ai"
)

// ChatRequest is the body POSTed by the AI SDK DefaultChatTransport. messages is
// the running UIMessage list; model and reasoningEffort are optional per-request
// overrides carried in the transport `body`.
type ChatRequest struct {
	ID              string      `json:"id,omitempty"`
	Messages        []UIMessage `json:"messages"`
	Model           string      `json:"model,omitempty"`
	ReasoningEffort Effort      `json:"reasoningEffort,omitempty"`
}

// UIMessage is the AI SDK v6 client message: a role plus typed parts. We consume
// text parts; tool parts in history are folded back into model turns by the SDK
// and are not required to reconstruct context for a new turn.
type UIMessage struct {
	ID    string   `json:"id,omitempty"`
	Role  string   `json:"role"`
	Parts []UIPart `json:"parts"`
}

// UIPart is one part of a UIMessage. Only the fields we read are modelled.
type UIPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// toGenkitMessages converts the UIMessage history into Genkit messages, keeping
// only user and assistant text. Empty messages are dropped.
func toGenkitMessages(msgs []UIMessage) ([]*ai.Message, error) {
	var out []*ai.Message
	for _, m := range msgs {
		text := textOf(m)
		if text == "" {
			continue
		}
		switch m.Role {
		case "user":
			out = append(out, ai.NewUserTextMessage(text))
		case "assistant":
			out = append(out, ai.NewModelTextMessage(text))
		case "system":
			out = append(out, ai.NewSystemTextMessage(text))
		default:
			return nil, fmt.Errorf("unknown message role %q", m.Role)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no messages with content")
	}
	return out, nil
}

func textOf(m UIMessage) string {
	var s string
	for _, p := range m.Parts {
		if p.Type == "text" {
			s += p.Text
		}
	}
	return s
}
