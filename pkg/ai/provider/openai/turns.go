package openai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/openai/openai-go/responses"
)

// emitTurn reports one message of the loop as an EventTurn; a model reply
// carries the usage of the response that produced it.
func (p *Provider) emitTurn(ctx context.Context, out chan<- ai.Event, message api.Message, usage *ai.Usage) {
	if len(message.Parts) == 0 {
		return
	}
	emit(ctx, out, ai.Event{Kind: ai.EventTurn, Model: p.model, Usage: usage, Message: &message})
}

// requestTurns is the input a run adds before its first response: the system
// and user prompt, or the conversation's messages after its last reply. A run
// resumed from a tool approval adds none — it continues the turn it parked.
func requestTurns(req ai.Request) []api.Message {
	if req.ToolApproval != nil {
		return nil
	}
	if len(req.Messages) > 0 {
		start := 0
		for i, message := range req.Messages {
			if message.Role == api.RoleAssistant {
				start = i + 1
			}
		}
		return req.Messages[start:]
	}
	var messages []api.Message
	if req.Prompt.System != "" {
		messages = append(messages, api.Message{Role: api.RoleSystem, Parts: []api.Part{{Type: api.PartText, Text: req.Prompt.System}}})
	}
	user := api.Message{Role: api.RoleUser}
	if req.Prompt.User != "" {
		user.Parts = append(user.Parts, api.Part{Type: api.PartText, Text: req.Prompt.User})
	}
	for i := range req.Prompt.Attachments {
		attachment := req.Prompt.Attachments[i]
		user.Parts = append(user.Parts, api.Part{Type: api.PartAttachment, Attachment: &attachment})
	}
	return append(messages, user)
}

// responseTurn maps a response's output items onto the model's reply.
func responseTurn(items []responses.ResponseOutputItemUnion) (api.Message, error) {
	reply := api.Message{Role: api.RoleAssistant}
	for _, item := range items {
		switch item.Type {
		case "reasoning":
			for _, summary := range item.Summary {
				if summary.Text != "" {
					reply.Parts = append(reply.Parts, api.Part{Type: api.PartReasoning, Text: summary.Text})
				}
			}
		case "message":
			for _, content := range item.Content {
				switch {
				case content.Type == "output_text" && content.Text != "":
					reply.Parts = append(reply.Parts, api.Part{Type: api.PartText, Text: content.Text})
				case content.Type == "refusal" && content.Refusal != "":
					reply.Parts = append(reply.Parts, api.Part{Type: api.PartText, Text: content.Refusal})
				}
			}
		case "function_call":
			input := json.RawMessage(item.Arguments)
			if !json.Valid(input) {
				encoded, err := json.Marshal(item.Arguments)
				if err != nil {
					return api.Message{}, fmt.Errorf("encode function %q arguments: %w", item.Name, err)
				}
				input = encoded
			}
			reply.Parts = append(reply.Parts, api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
				ToolCallID: item.CallID, Name: item.Name, Input: input,
			}})
		default:
			return api.Message{}, fmt.Errorf("OpenAI returned unsupported output item %q", item.Type)
		}
	}
	return reply, nil
}

// toolTurn is the results of one reply's calls, as the model reads them next.
func toolTurn(resolved []resolvedCall) api.Message {
	message := api.Message{Role: api.RoleTool}
	for _, call := range resolved {
		if call.result != nil {
			message.Parts = append(message.Parts, api.Part{Type: api.PartToolResult, ToolResult: call.result})
		}
	}
	return message
}
