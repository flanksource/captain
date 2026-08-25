package openai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
)

const (
	approvalCheckpointCodec   = "openai-responses-input-json"
	approvalCheckpointVersion = 1
)

// approvalCheckpoint is the durable, stateless Responses input needed to resume
// after Captain persists a tool-approval interruption.
type approvalCheckpoint struct {
	Instructions string                       `json:"instructions,omitempty"`
	Input        responses.ResponseInputParam `json:"input"`
}

func approvalState(
	req ai.Request,
	instructions param.Opt[string],
	history responses.ResponseInputParam,
	response *responses.Response,
	calls []functionCall,
	resolved []resolvedCall,
) (*api.ToolApprovalState, error) {
	messages, err := approvalRequestMessages(req)
	if err != nil {
		return nil, err
	}
	assistant := responseAssistantMessage(response.Output)
	checkpoint, err := encodeApprovalCheckpoint(instructions, history)
	if err != nil {
		return nil, err
	}
	state := &api.ToolApprovalState{
		Messages:           append(messages, assistant),
		Calls:              make([]api.ToolApprovalCall, 0, len(calls)),
		ProviderCheckpoint: checkpoint,
	}
	for i, call := range calls {
		entry := api.ToolApprovalCall{Request: api.ToolApprovalRequest{
			ToolCallID: call.ID, Tool: call.Name, Input: json.RawMessage(call.Arguments),
		}}
		if resolved[i].result != nil {
			entry.Result = resolved[i].result
		}
		state.Calls = append(state.Calls, entry)
	}
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("openai approval state: %w", err)
	}
	return state, nil
}

func approvalRequestMessages(req ai.Request) ([]api.Message, error) {
	if len(req.Messages) > 0 {
		return append([]api.Message(nil), req.Messages...), nil
	}
	messages := make([]api.Message, 0, 2)
	if req.Prompt.System != "" {
		messages = append(messages, api.Message{Role: api.RoleSystem, Parts: []api.Part{{Type: api.PartText, Text: req.Prompt.System}}})
	}
	parts := make([]api.Part, 0, len(req.Prompt.Attachments))
	if req.Prompt.User != "" {
		parts = append(parts, api.Part{Type: api.PartText, Text: req.Prompt.User})
	}
	for i := range req.Prompt.Attachments {
		attachment := req.Prompt.Attachments[i]
		parts = append(parts, api.Part{Type: api.PartAttachment, Attachment: &attachment})
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("openai approval state has no user prompt")
	}
	return append(messages, api.Message{Role: api.RoleUser, Parts: parts}), nil
}

func responseAssistantMessage(items []responses.ResponseOutputItemUnion) api.Message {
	message := api.Message{Role: api.RoleAssistant}
	for _, item := range items {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" && content.Text != "" {
					message.Parts = append(message.Parts, api.Part{Type: api.PartText, Text: content.Text})
				}
			}
		case "reasoning":
			for _, summary := range item.Summary {
				if summary.Text != "" {
					message.Parts = append(message.Parts, api.Part{Type: api.PartReasoning, Text: summary.Text})
				}
			}
		case "function_call":
			message.Parts = append(message.Parts, api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
				ToolCallID: item.CallID, Name: item.Name, Input: json.RawMessage(item.Arguments),
			}})
		}
	}
	return message
}

// The checkpoint persists the complete native input, including encrypted
// reasoning items, so an approval can resume without provider-side storage.
func encodeApprovalCheckpoint(instructions param.Opt[string], input responses.ResponseInputParam) (*api.ProviderCheckpoint, error) {
	checkpoint := approvalCheckpoint{Input: input}
	if instructions.Valid() {
		checkpoint.Instructions = instructions.Value
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI approval checkpoint: %w", err)
	}
	return &api.ProviderCheckpoint{Codec: approvalCheckpointCodec, Version: approvalCheckpointVersion, Payload: payload}, nil
}

func decodeApprovalCheckpoint(resume *api.ToolApprovalResume) (param.Opt[string], responses.ResponseInputParam, error) {
	if resume == nil {
		return param.Opt[string]{}, nil, fmt.Errorf("OpenAI tool approval resume is required")
	}
	if err := resume.Validate(); err != nil {
		return param.Opt[string]{}, nil, err
	}
	checkpoint := resume.State.ProviderCheckpoint
	if checkpoint == nil {
		return param.Opt[string]{}, nil, fmt.Errorf("OpenAI approval checkpoint is missing")
	}
	if checkpoint.Codec != approvalCheckpointCodec || checkpoint.Version != approvalCheckpointVersion {
		return param.Opt[string]{}, nil, fmt.Errorf("unsupported OpenAI approval checkpoint %q version %d", checkpoint.Codec, checkpoint.Version)
	}
	var decoded approvalCheckpoint
	if err := json.Unmarshal(checkpoint.Payload, &decoded); err != nil {
		return param.Opt[string]{}, nil, fmt.Errorf("decode OpenAI approval checkpoint: %w", err)
	}
	var instructions param.Opt[string]
	if decoded.Instructions != "" {
		instructions = openaisdk.String(decoded.Instructions)
	}
	return instructions, decoded.Input, nil
}

// resumeCalls converts persisted decisions into function outputs. Only approved
// pending calls execute locally; resolved siblings and external responses are
// never run a second time.
func (p *Provider) resumeCalls(ctx context.Context, resume *api.ToolApprovalResume, state *requestState, out chan<- ai.Event) ([]responses.ResponseInputItemUnionParam, error) {
	decisions := make(map[string]api.ToolApprovalDecision, len(resume.Decisions))
	for _, decision := range resume.Decisions {
		decisions[decision.ToolCallID] = decision
	}
	checkpointCalls := checkpointFunctionCalls(state.history)
	outputs := make([]responses.ResponseInputItemUnionParam, 0, len(resume.State.Calls))
	for _, call := range resume.State.Calls {
		native, ok := checkpointCalls[call.Request.ToolCallID]
		if !ok || native.Name != call.Request.Tool || !equalJSON([]byte(native.Arguments), call.Request.Input) {
			return nil, fmt.Errorf("OpenAI approval checkpoint does not match tool call %q", call.Request.ToolCallID)
		}
		if call.Result != nil {
			outputs = append(outputs, responses.ResponseInputItemParamOfFunctionCallOutput(call.Request.ToolCallID, resultOutput(call.Result)))
			continue
		}
		decision := decisions[call.Request.ToolCallID]
		switch decision.Action {
		case api.ToolApprovalDeny:
			reason := decision.Message
			if reason == "" {
				reason = "tool call denied"
			}
			outputs = append(outputs, responses.ResponseInputItemParamOfFunctionCallOutput(call.Request.ToolCallID, jsonText(map[string]any{"denied": true, "reason": reason})))
		case api.ToolApprovalRespond:
			outputs = append(outputs, responses.ResponseInputItemParamOfFunctionCallOutput(call.Request.ToolCallID, resultOutput(decision.Result)))
		case api.ToolApprovalApprove:
			definition, ok := state.byName[call.Request.Tool]
			if !ok {
				return nil, fmt.Errorf("approved OpenAI function %q is no longer available", call.Request.Tool)
			}
			raw := call.Request.Input
			if len(decision.Input) > 0 {
				raw = decision.Input
			}
			args, err := callArguments(string(raw))
			if err != nil {
				return nil, fmt.Errorf("approved OpenAI function %q: %w", call.Request.Tool, err)
			}
			if out != nil {
				emit(ctx, out, ai.Event{Kind: ai.EventToolUse, Tool: call.Request.Tool, Input: args, ToolCallID: call.Request.ToolCallID, Model: p.model})
			}
			value, err := definition.Handler(ctx, args)
			if err != nil {
				if out != nil {
					emit(ctx, out, ai.Event{Kind: ai.EventToolResult, Tool: call.Request.Tool, ToolCallID: call.Request.ToolCallID, Success: false, Text: err.Error(), Model: p.model})
				}
				value = map[string]any{"error": err.Error()}
			} else if out != nil {
				emit(ctx, out, ai.Event{Kind: ai.EventToolResult, Tool: call.Request.Tool, ToolCallID: call.Request.ToolCallID, Success: true, Text: toolOutputText(value), Model: p.model})
			}
			outputs = append(outputs, responses.ResponseInputItemParamOfFunctionCallOutput(call.Request.ToolCallID, modelOutput(value)))
		}
	}
	return outputs, nil
}

func checkpointFunctionCalls(input responses.ResponseInputParam) map[string]functionCall {
	calls := map[string]functionCall{}
	for _, item := range input {
		if item.OfFunctionCall != nil {
			call := item.OfFunctionCall
			calls[call.CallID] = functionCall{ID: call.CallID, Name: call.Name, Arguments: call.Arguments}
		}
	}
	return calls
}

func equalJSON(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && jsonText(a) == jsonText(b)
}
