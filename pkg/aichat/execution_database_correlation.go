package aichat

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

const providerToolCorrelationTTL = 5 * time.Second

func (e *databaseExecution) rememberProviderToolUse(event api.Event) {
	e.mu.Lock()
	e.providerToolUses = append(e.providerToolUses, event)
	e.mu.Unlock()
	select {
	case e.providerToolUseReady <- struct{}{}:
	default:
	}
}

func (e *databaseExecution) claimProviderToolUse(ctx context.Context, request api.PermissionRequest) (string, error) {
	timer := time.NewTimer(providerToolCorrelationTTL)
	defer timer.Stop()
	for {
		e.mu.Lock()
		for i, event := range e.providerToolUses {
			if event.Tool != request.Tool || !reflect.DeepEqual(event.Input, request.Input) {
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

func (e *databaseExecution) createProviderApproval(ctx context.Context, event api.Event) (*database.TurnRequest, error) {
	if event.ToolCallID == "" || event.Tool == "" {
		return nil, fmt.Errorf("provider approval requires a tool call ID and tool name")
	}
	request, err := e.db.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
		SessionID: e.session.ID, TurnID: e.turn.ID, PromptRunID: e.run.ID, ModelCallID: e.modelCallID,
		ToolCallID: event.ToolCallID, Tool: event.Tool, Input: event.Input,
		RequestedBy: "provider", ExpiresAt: time.Now().Add(providerApprovalTimeout),
	})
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.approvalIDs[event.ToolCallID] = request.ID
	e.mu.Unlock()
	return request, nil
}
