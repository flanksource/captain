package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/flanksource/captain/pkg/api"
)

func (s *Service) resolveToolApproval(ctx context.Context, request *ChatRequest) error {
	if request.ToolApproval != nil || len(request.Messages) == 0 {
		return nil
	}
	message := request.Messages[len(request.Messages)-1]
	if message.Role != string(api.RoleAssistant) {
		return nil
	}
	responded := make(map[string]UIPart)
	var stateData json.RawMessage
	for _, part := range message.Parts {
		if part.Type == "data-tool-approval" {
			stateData = part.Data
		}
		if !part.IsTool() || part.State != "approval-responded" {
			continue
		}
		if part.ToolCallID == "" {
			return fmt.Errorf("approval response has no tool call ID")
		}
		if _, exists := responded[part.ToolCallID]; exists {
			return fmt.Errorf("duplicate approval response for tool call %q", part.ToolCallID)
		}
		responded[part.ToolCallID] = part
	}
	if len(responded) == 0 {
		return nil
	}
	if request.ThreadID != "" && s.options.Threads != nil {
		thread, err := s.options.Threads.Get(ctx, request.ThreadID)
		if err != nil {
			return fmt.Errorf("load durable tool approval state: %w", err)
		}
		if len(thread.Messages) == 0 {
			return fmt.Errorf("thread %q has no durable tool approval state", request.ThreadID)
		}
		stored := thread.Messages[len(thread.Messages)-1]
		if stored.Role != string(api.RoleAssistant) {
			return fmt.Errorf("thread %q does not end with a durable assistant approval", request.ThreadID)
		}
		if message.ID != "" && stored.ID != "" && message.ID != stored.ID {
			return fmt.Errorf("approval response message ID %q does not match stored message %q", message.ID, stored.ID)
		}
		stateData = nil
		for _, part := range stored.Parts {
			if part.Type == "data-tool-approval" {
				stateData = part.Data
			}
		}
	}
	if len(stateData) == 0 {
		return fmt.Errorf("approval response is missing durable tool approval state")
	}
	var state api.ToolApprovalState
	if err := json.Unmarshal(stateData, &state); err != nil {
		return fmt.Errorf("decode durable tool approval state: %w", err)
	}
	if err := state.Validate(); err != nil {
		return fmt.Errorf("validate durable tool approval state: %w", err)
	}
	decisions := make([]api.ToolApprovalDecision, 0, len(responded))
	matched := make(map[string]bool, len(responded))
	for _, pending := range state.Pending() {
		part, ok := responded[pending.ToolCallID]
		if !ok {
			return fmt.Errorf("pending tool call %q has no approval response", pending.ToolCallID)
		}
		if err := validateApprovalPart(pending, part); err != nil {
			return err
		}
		action := api.ToolApprovalDeny
		if *part.Approval.Approved {
			action = api.ToolApprovalApprove
		}
		decisions = append(decisions, api.ToolApprovalDecision{
			ToolCallID: pending.ToolCallID,
			Tool:       pending.Tool,
			Action:     action,
			Message:    part.Approval.Reason,
		})
		if action == api.ToolApprovalApprove {
			decisions[len(decisions)-1].Message = ""
		}
		matched[pending.ToolCallID] = true
	}
	for id := range responded {
		if !matched[id] {
			return fmt.Errorf("approval response references non-pending tool call %q", id)
		}
	}
	resume := &api.ToolApprovalResume{State: state, Decisions: decisions}
	if err := resume.Validate(); err != nil {
		return fmt.Errorf("validate tool approval resume: %w", err)
	}
	request.ToolApproval = resume
	return nil
}

func validateApprovalPart(pending api.ToolApprovalRequest, part UIPart) error {
	if part.Approval == nil || part.Approval.Approved == nil {
		return fmt.Errorf("tool call %q has no completed approval response", pending.ToolCallID)
	}
	if part.Approval.ID != pending.ToolCallID {
		return fmt.Errorf("tool call %q approval ID is %q", pending.ToolCallID, part.Approval.ID)
	}
	if part.EffectiveToolName() != pending.Tool {
		return fmt.Errorf(
			"tool call %q approval names %q, want %q",
			pending.ToolCallID, part.EffectiveToolName(), pending.Tool,
		)
	}
	if !equalPartJSON(part.Input, pending.Input) {
		return fmt.Errorf("tool call %q approval input does not match durable state", pending.ToolCallID)
	}
	return nil
}

func equalPartJSON(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil &&
		json.Unmarshal(right, &rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}
