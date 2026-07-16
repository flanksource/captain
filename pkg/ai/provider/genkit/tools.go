package genkit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
)

// maxToolTurns caps the model↔tool iteration so a misbehaving loop terminates.
const maxToolTurns = 16

// SupportsCallerTools reports that the genkit provider can expose and execute
// api.Config.Tools. It satisfies api.ToolCapableProvider.
func (p *Provider) SupportsCallerTools() bool { return true }

// toolOptions builds the genkit generate options for the caller-supplied tools
// on the provider config. Each tool runs in-process via its handler; emit, when
// non-nil, receives EventToolUse / EventPermission / EventToolResult as the
// model calls them. Returns nil when there are no caller tools, so a run with
// none is byte-for-byte unchanged.
func (p *Provider) toolOptions(emit func(ai.Event)) []gkai.GenerateOption {
	if len(p.cfg.Tools) == 0 {
		return nil
	}
	refs := make([]gkai.ToolRef, 0, len(p.cfg.Tools))
	for i := range p.cfg.Tools {
		refs = append(refs, p.genkitTool(p.cfg.Tools[i], emit))
	}
	return []gkai.GenerateOption{gkai.WithTools(refs...), gkai.WithMaxTurns(maxToolTurns)}
}

// genkitTool wraps one api.ToolDefinition as an ephemeral genkit tool. Ephemeral
// (NewToolWithInputSchema, passed via WithTools) rather than genkit.DefineTool:
// the genkit instance is cached and shared per (backend, apiKey), so registering
// tools globally would leak them across unrelated requests.
func (p *Provider) genkitTool(def api.ToolDefinition, emit func(ai.Event)) gkai.ToolRef {
	handler := func(tc *gkai.ToolContext, input any) (any, error) {
		return p.runTool(tc.Context, def, input, emit)
	}
	return gkai.NewToolWithInputSchema[any](def.Name, def.Description, def.InputSchema, handler)
}

// runTool gates a caller tool through Config.CanUseTool (when it needs approval),
// runs its handler, and publishes the use/permission/result events. On a denied
// or errored call it feeds a message back to the model as the tool result so the
// model can react rather than the whole generation failing.
func (p *Provider) runTool(ctx context.Context, def api.ToolDefinition, input any, emit func(ai.Event)) (any, error) {
	args := toArgsMap(input)
	callID := fmt.Sprintf("tool-%d", p.toolCallSeq.Add(1))

	if emit != nil {
		emit(ai.Event{Kind: ai.EventToolUse, Tool: def.Name, Input: args, ToolCallID: callID, Model: p.cfg.Model.Name})
	}

	if def.NeedsApproval() && p.cfg.CanUseTool != nil {
		if emit != nil {
			emit(ai.Event{Kind: ai.EventPermission, Tool: def.Name, Input: args, ToolCallID: callID, Model: p.cfg.Model.Name})
		}
		decision, err := p.cfg.CanUseTool(ctx, api.PermissionRequest{
			Tool:      def.Name,
			Input:     args,
			ToolUseID: callID,
			SessionID: p.cfg.SessionID,
		})
		if err != nil {
			return p.toolDenied(def.Name, callID, err.Error(), emit)
		}
		if !decision.Allow {
			msg := decision.Message
			if msg == "" {
				msg = "tool call denied"
			}
			return p.toolDenied(def.Name, callID, msg, emit)
		}
		if decision.UpdatedInput != nil {
			args = decision.UpdatedInput
		}
	}

	out, err := def.Handler(ctx, args)
	if err != nil {
		if emit != nil {
			emit(ai.Event{Kind: ai.EventToolResult, Tool: def.Name, ToolCallID: callID, Success: false, Text: err.Error(), Model: p.cfg.Model.Name})
		}
		// Feed the error back as the tool result rather than aborting the turn.
		return map[string]any{"error": err.Error()}, nil
	}

	if emit != nil {
		emit(ai.Event{Kind: ai.EventToolResult, Tool: def.Name, ToolCallID: callID, Success: true, Text: toolOutputText(out), Model: p.cfg.Model.Name})
	}
	return out, nil
}

// toolDenied emits a failed EventToolResult and returns the deny reason to the
// model as the tool output (not an error, so the model can adapt).
func (p *Provider) toolDenied(name, callID, reason string, emit func(ai.Event)) (any, error) {
	if emit != nil {
		emit(ai.Event{Kind: ai.EventToolResult, Tool: name, ToolCallID: callID, Success: false, Text: reason, Model: p.cfg.Model.Name})
	}
	return map[string]any{"denied": true, "reason": reason}, nil
}

// toArgsMap normalizes genkit's decoded tool input into a JSON object map.
func toArgsMap(input any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	if m, ok := input.(map[string]any); ok {
		return m
	}
	data, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// toolOutputText renders a handler's return value as the EventToolResult text.
func toolOutputText(out any) string {
	if out == nil {
		return ""
	}
	if s, ok := out.(string); ok {
		return s
	}
	data, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf("%v", out)
	}
	return string(data)
}

// static assertion: the genkit provider advertises caller-tool support.
var _ api.ToolCapableProvider = (*Provider)(nil)
