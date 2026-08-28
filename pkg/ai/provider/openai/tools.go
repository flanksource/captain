package openai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	"github.com/openai/openai-go/responses"
)

type functionCall struct {
	ID        string
	Name      string
	Arguments string
}

type resolvedCall struct {
	wire   responses.ResponseInputItemUnionParam
	result *api.ToolResult
}

func functionCalls(items []responses.ResponseOutputItemUnion) []functionCall {
	calls := make([]functionCall, 0)
	for _, item := range items {
		if item.Type == "function_call" {
			calls = append(calls, functionCall{ID: item.CallID, Name: item.Name, Arguments: item.Arguments})
		}
	}
	return calls
}

func (p *Provider) resolveCalls(
	ctx context.Context,
	req ai.Request,
	state *requestState,
	response *responses.Response,
	calls []functionCall,
	out chan<- ai.Event,
) ([]responses.ResponseInputItemUnionParam, *api.ToolApprovalState, error) {
	resolved := make([]resolvedCall, 0, len(calls))
	pending := false
	for _, call := range calls {
		definition, ok := state.byName[call.Name]
		if !ok {
			return nil, nil, fmt.Errorf("OpenAI called unknown function %q", call.Name)
		}
		args, err := callArguments(call.Arguments)
		if err != nil {
			value := map[string]any{"error": err.Error()}
			resolved = append(resolved, callResult(call.ID, value))
			continue
		}

		emit(ctx, out, ai.Event{Kind: ai.EventToolUse, Tool: call.Name, Input: args, ToolCallID: call.ID, Model: p.model})
		if definition.NeedsApproval() {
			emit(ctx, out, ai.Event{Kind: ai.EventPermission, Tool: call.Name, Input: args, ToolCallID: call.ID, Model: p.model})
			if p.cfg.CanUseTool == nil {
				pending = true
				resolved = append(resolved, resolvedCall{})
				continue
			}
			decision, decisionErr := p.cfg.CanUseTool(ctx, api.PermissionRequest{
				Tool: call.Name, Input: args, ToolUseID: call.ID, SessionID: p.cfg.SessionID,
			})
			if decisionErr != nil || !decision.Allow {
				reason := "tool call denied"
				if decisionErr != nil {
					reason = decisionErr.Error()
				} else if decision.Message != "" {
					reason = decision.Message
				}
				emit(ctx, out, ai.Event{Kind: ai.EventToolResult, Tool: call.Name, ToolCallID: call.ID, Success: false, Text: reason, Model: p.model})
				resolved = append(resolved, callResult(call.ID, map[string]any{"denied": true, "reason": reason}))
				continue
			}
			if decision.UpdatedInput != nil {
				args = decision.UpdatedInput
			}
		}

		value, err := definition.Handler(ctx, args)
		if err != nil {
			emit(ctx, out, ai.Event{Kind: ai.EventToolResult, Tool: call.Name, ToolCallID: call.ID, Success: false, Text: err.Error(), Model: p.model})
			resolved = append(resolved, callResult(call.ID, map[string]any{"error": err.Error()}))
			continue
		}
		emit(ctx, out, ai.Event{Kind: ai.EventToolResult, Tool: call.Name, ToolCallID: call.ID, Success: true, Text: toolOutputText(value), Model: p.model})
		resolved = append(resolved, callResult(call.ID, value))
	}

	if pending {
		approval, err := approvalState(req, state.params.Instructions, state.history, response, calls, resolved)
		return nil, approval, err
	}
	outputs := make([]responses.ResponseInputItemUnionParam, 0, len(resolved))
	for _, call := range resolved {
		outputs = append(outputs, call.wire)
	}
	return outputs, nil, nil
}

func callArguments(raw string) (map[string]any, error) {
	if raw == "" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("invalid function arguments: %w", err)
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func callResult(callID string, value any) resolvedCall {
	encoded, err := json.Marshal(value)
	if err != nil {
		message := fmt.Sprintf("marshal tool result: %v", err)
		value = map[string]any{"error": message}
		encoded, _ = json.Marshal(value)
	}
	return resolvedCall{
		wire:   responses.ResponseInputItemParamOfFunctionCallOutput(callID, modelOutput(value)),
		result: &api.ToolResult{ToolCallID: callID, Output: encoded},
	}
}

func modelOutput(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return jsonText(value)
}

func jsonText(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func toolOutputText(value any) string {
	if value == nil {
		return ""
	}
	return modelOutput(value)
}

func responseRefusal(items []responses.ResponseOutputItemUnion) string {
	for _, item := range items {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "refusal" && content.Refusal != "" {
				return content.Refusal
			}
		}
	}
	return ""
}

// responseOutputParams preserves message, function, and encrypted reasoning
// items so subsequent tool turns remain stateless without losing model context.
func responseOutputParams(items []responses.ResponseOutputItemUnion) ([]responses.ResponseInputItemUnionParam, error) {
	output := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case "message":
			value := item.AsMessage().ToParam()
			output = append(output, responses.ResponseInputItemUnionParam{OfOutputMessage: &value})
		case "function_call":
			value := item.AsFunctionCall().ToParam()
			output = append(output, responses.ResponseInputItemUnionParam{OfFunctionCall: &value})
		case "reasoning":
			value := item.AsReasoning().ToParam()
			output = append(output, responses.ResponseInputItemUnionParam{OfReasoning: &value})
		default:
			return nil, fmt.Errorf("OpenAI returned unsupported output item %q", item.Type)
		}
	}
	return output, nil
}
