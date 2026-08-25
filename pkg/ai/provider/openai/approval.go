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
// after Captain persists a tool-approval interruption. Output items remain raw
// so the SDK decodes them through its response union rather than its ambiguous
// request union.
type approvalCheckpoint struct {
	Instructions string                   `json:"instructions,omitempty"`
	Input        []approvalCheckpointItem `json:"input"`
}

// approvalCheckpointItem records which SDK union must decode each wire item.
type approvalCheckpointItem struct {
	Request json.RawMessage `json:"request,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
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
// Response output stays tagged because message request and response variants
// share type:"message" and cannot safely round-trip through the SDK param union.
func encodeApprovalCheckpoint(instructions param.Opt[string], input responses.ResponseInputParam) (*api.ProviderCheckpoint, error) {
	checkpoint := approvalCheckpoint{Input: make([]approvalCheckpointItem, len(input))}
	if instructions.Valid() {
		checkpoint.Instructions = instructions.Value
	}
	for i, item := range input {
		payload, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("encode OpenAI approval checkpoint item %d: %w", i+1, err)
		}
		if item.OfOutputMessage != nil || item.OfFunctionCall != nil || item.OfReasoning != nil {
			checkpoint.Input[i].Output = payload
		} else {
			checkpoint.Input[i].Request = payload
		}
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
	input := make(responses.ResponseInputParam, 0, len(decoded.Input))
	for i, item := range decoded.Input {
		switch {
		case len(item.Request) > 0 && len(item.Output) == 0:
			var request responses.ResponseInputItemUnionParam
			if err := json.Unmarshal(item.Request, &request); err != nil {
				return param.Opt[string]{}, nil, fmt.Errorf("decode OpenAI approval checkpoint request item %d: %w", i+1, err)
			}
			input = append(input, request)
		case len(item.Output) > 0 && len(item.Request) == 0:
			var output responses.ResponseOutputItemUnion
			if err := json.Unmarshal(item.Output, &output); err != nil {
				return param.Opt[string]{}, nil, fmt.Errorf("decode OpenAI approval checkpoint output item %d: %w", i+1, err)
			}
			params, err := responseOutputParams([]responses.ResponseOutputItemUnion{output})
			if err != nil {
				return param.Opt[string]{}, nil, fmt.Errorf("decode OpenAI approval checkpoint output item %d: %w", i+1, err)
			}
			input = append(input, params[0])
		default:
			return param.Opt[string]{}, nil, fmt.Errorf("decode OpenAI approval checkpoint item %d: expected exactly one request or output payload", i+1)
		}
	}
	return instructions, input, nil
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

// checkpointFunctionCalls reads the wire form because SDK response parameters
// keep their values in override metadata rather than their exported fields.
func checkpointFunctionCalls(input responses.ResponseInputParam) map[string]functionCall {
	calls := map[string]functionCall{}
	for _, item := range input {
		if item.OfFunctionCall == nil {
			continue
		}
		payload, err := json.Marshal(item)
		if err != nil {
			continue
		}
		var call responses.ResponseFunctionToolCall
		if err := json.Unmarshal(payload, &call); err != nil || call.CallID == "" {
			continue
		}
		calls[call.CallID] = functionCall{ID: call.CallID, Name: call.Name, Arguments: call.Arguments}
	}
	return calls
}

func equalJSON(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && jsonText(a) == jsonText(b)
}
