package genkit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"

	gkai "github.com/firebase/genkit/go/ai"
)

// turnLog follows one Generate call's tool loop from inside genkit's model hook.
// It reports every message the loop exchanges with the model as an EventTurn
// and totals the usage of every reply.
//
// The hook is the only place either is visible: genkit returns just the last
// reply's usage, and nothing at all when that reply's output then fails
// validation — which is exactly the run that most needs its spend and its
// exchange on record.
type turnLog struct {
	provider *ai.ModelProvider
	model    string
	emit     func(ai.Event)

	mu       sync.Mutex
	usage    ai.Usage
	hasUsage bool
}

func newTurnLog(provider *ai.ModelProvider, model string, emit func(ai.Event)) *turnLog {
	return &turnLog{provider: provider, model: model, emit: emit}
}

// total is every reply's usage summed, or nil when no reply reported any.
func (t *turnLog) total() *ai.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasUsage {
		return nil
	}
	usage := t.usage
	return &usage
}

func (t *turnLog) middleware(context.Context) (*gkai.Hooks, error) {
	return &gkai.Hooks{
		WrapModel: func(ctx context.Context, params *gkai.ModelParams, next gkai.ModelNext) (*gkai.ModelResponse, error) {
			if err := t.sent(params.Request.Messages); err != nil {
				return nil, err
			}
			response, err := next(ctx, params)
			if response != nil {
				if replyErr := t.replied(response); replyErr != nil {
					return nil, replyErr
				}
				response.Request = &gkai.ModelRequest{Messages: cloneCheckpointMessages(params.Request.Messages)}
			}
			return response, err
		},
		WrapTool: func(ctx context.Context, params *gkai.ToolParams, next gkai.ToolNext) (*gkai.MultipartToolResponse, error) {
			if params == nil || params.Request == nil {
				return nil, fmt.Errorf("genkit tool middleware received no provider request")
			}
			return next(context.WithValue(ctx, genkitToolRequestContextKey{}, params.Request), params)
		},
	}, nil
}

// sent reports the input a request adds: the messages after the last model
// reply. The loop's first request adds the system and user prompt, each later
// one the previous reply's tool results, and a continued conversation its new
// user turn — while the history before it was reported when it happened.
func (t *turnLog) sent(messages []*gkai.Message) error {
	start := 0
	for i, message := range messages {
		if message != nil && message.Role == gkai.RoleModel {
			start = i + 1
		}
	}
	for _, message := range messages[start:] {
		if err := t.report(message, nil); err != nil {
			return err
		}
	}
	return nil
}

// replied reports the model's reply and adds its usage to the total. Tool calls
// a provider named no id for get one here, before genkit would assign its own,
// so the call and its result correlate in what is reported.
func (t *turnLog) replied(response *gkai.ModelResponse) error {
	var usage *ai.Usage
	if response.Usage != nil {
		mapped := mapUsage(response.Usage, t.provider)
		usage = &mapped
		t.mu.Lock()
		t.usage, t.hasUsage = t.usage.Add(mapped), true
		t.mu.Unlock()
	}
	if response.Message == nil {
		return nil
	}
	for _, part := range response.Message.Content {
		if part.IsToolRequest() && part.ToolRequest.Ref == "" {
			part.ToolRequest.Ref = uuid.NewString()
		}
	}
	return t.report(response.Message, usage)
}

func (t *turnLog) report(message *gkai.Message, usage *ai.Usage) error {
	if t.emit == nil || message == nil {
		return nil
	}
	converted, err := turnMessage(message)
	if err != nil {
		return fmt.Errorf("genkit %s turn: %w", t.provider.Name, err)
	}
	if len(converted.Parts) == 0 {
		return nil
	}
	t.emit(ai.Event{Kind: ai.EventTurn, Model: t.model, Usage: usage, Message: &converted})
	return nil
}

// turnMessage maps a genkit message onto the canonical message it reports.
func turnMessage(message *gkai.Message) (api.Message, error) {
	role, err := turnRole(message.Role)
	if err != nil {
		return api.Message{}, err
	}
	out := api.Message{Role: role, Parts: make([]api.Part, 0, len(message.Content))}
	for i, part := range message.Content {
		converted, keep, err := turnPart(part)
		if err != nil {
			return api.Message{}, fmt.Errorf("%s message part %d: %w", message.Role, i+1, err)
		}
		if keep {
			out.Parts = append(out.Parts, converted)
		}
	}
	return out, nil
}

func turnRole(role gkai.Role) (api.MessageRole, error) {
	switch role {
	case gkai.RoleSystem:
		return api.RoleSystem, nil
	case gkai.RoleUser:
		return api.RoleUser, nil
	case gkai.RoleModel:
		return api.RoleAssistant, nil
	case gkai.RoleTool:
		return api.RoleTool, nil
	default:
		return "", fmt.Errorf("unsupported message role %q", role)
	}
}

// turnPart maps one part. Empty text and reasoning (a signature-only thinking
// block) carry nothing to report and are dropped. Media is reported by its type
// only: the bytes belong to the attachment store, not to a transcript.
func turnPart(part *gkai.Part) (api.Part, bool, error) {
	switch {
	case part.IsText():
		return api.Part{Type: api.PartText, Text: part.Text}, part.Text != "", nil
	case part.IsReasoning():
		return api.Part{Type: api.PartReasoning, Text: part.Text}, part.Text != "", nil
	case part.IsMedia():
		return api.Part{Type: api.PartText, Text: fmt.Sprintf("[%s attachment]", part.ContentType)}, true, nil
	case part.IsToolRequest():
		input, err := json.Marshal(part.ToolRequest.Input)
		if err != nil {
			return api.Part{}, false, fmt.Errorf("marshal tool %q input: %w", part.ToolRequest.Name, err)
		}
		return api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
			ToolCallID: part.ToolRequest.Ref, Name: part.ToolRequest.Name, Input: input,
		}}, true, nil
	case part.IsToolResponse():
		output, err := json.Marshal(part.ToolResponse.Output)
		if err != nil {
			return api.Part{}, false, fmt.Errorf("marshal tool %q output: %w", part.ToolResponse.Name, err)
		}
		return api.Part{Type: api.PartToolResult, ToolResult: &api.ToolResult{
			ToolCallID: part.ToolResponse.Ref, Output: output,
		}}, true, nil
	default:
		return api.Part{}, false, fmt.Errorf("unsupported content of genkit part kind %d", part.Kind)
	}
}
