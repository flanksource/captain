package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

const providerToolCorrelationTTL = 5 * time.Second

func (e *databaseExecution) rememberProviderToolUse(event api.Event) {
	e.mu.Lock()
	e.providerToolUses = append(e.providerToolUses, event)
	if e.toolUsesByID == nil {
		e.toolUsesByID = map[string]api.Event{}
	}
	e.toolUsesByID[event.ToolCallID] = event
	e.mu.Unlock()
	select {
	case e.providerToolUseReady <- struct{}{}:
	default:
	}
}

func (e *databaseExecution) claimProviderToolUse(ctx context.Context, request api.ApprovalRequest) (string, error) {
	timer := time.NewTimer(providerToolCorrelationTTL)
	defer timer.Stop()
	for {
		e.mu.Lock()
		for i, event := range e.providerToolUses {
			if !sameProviderToolCall(event, request.Tool, request.Input) {
				continue
			}
			e.providerToolUses = append(e.providerToolUses[:i], e.providerToolUses[i+1:]...)
			e.mu.Unlock()
			return event.ToolCallID, nil
		}
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return "", fmt.Errorf("caller tool %q did not match a provider tool use within %s", request.Tool, providerToolCorrelationTTL)
		case <-e.providerToolUseReady:
		}
	}
}

// sameProviderToolCall is how a caller-tool call that carried no provider
// tool-use ID is matched to the provider's tool use: same tool, same input.
// Approval claims and display joins share it so they correlate identically.
func sameProviderToolCall(use api.Event, tool string, input map[string]any) bool {
	return use.Tool == tool && reflect.DeepEqual(use.Input, input)
}

// rememberCallerDisplay keeps a caller tool's display value until the
// provider reports the result of the same call.
func (e *databaseExecution) rememberCallerDisplay(result callertools.CallResult) {
	e.mu.Lock()
	e.callerDisplays = append(e.callerDisplays, result)
	e.mu.Unlock()
}

// takeCallerDisplay returns the display value for a provider tool result,
// matching by tool-use ID first and by tool and input when the caller-tool
// runtime had to generate the ID. Each value is handed out once.
func (e *databaseExecution) takeCallerDisplay(event api.Event) json.RawMessage {
	e.mu.Lock()
	defer e.mu.Unlock()
	use, hasUse := e.toolUsesByID[event.ToolCallID]
	delete(e.toolUsesByID, event.ToolCallID)
	for i, result := range e.callerDisplays {
		matched := !result.ToolUseIDGenerated && result.ToolUseID == event.ToolCallID
		if !matched && result.ToolUseIDGenerated && hasUse {
			matched = sameProviderToolCall(use, result.Tool, result.Input)
		}
		if matched {
			e.callerDisplays = append(e.callerDisplays[:i], e.callerDisplays[i+1:]...)
			return result.Output
		}
	}
	return nil
}

func (e *databaseExecution) createProviderApproval(ctx context.Context, event api.Event) (*database.TurnRequest, error) {
	if event.ToolCallID == "" || event.Tool == "" {
		return nil, fmt.Errorf("provider approval requires a tool call ID and tool name")
	}
	request, err := e.db.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
		SessionID: e.session.ID, TurnID: e.turn.ID, PromptRunID: e.run.ID, ModelCallID: e.modelCallID,
		ToolCallID: event.ToolCallID, Tool: event.Tool, Input: event.Input,
		RequestedBy: "provider", ExpiresAt: time.Now().Add(approval.ProviderTimeout),
	})
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.approvalIDs[event.ToolCallID] = request.ID
	e.mu.Unlock()
	return request, nil
}
