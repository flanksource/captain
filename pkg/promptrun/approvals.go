package promptrun

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/api"
)

func validateApprovalOptions(in Input, requireRecord bool) error {
	if in.Approvals == nil {
		return nil
	}
	if strings.TrimSpace(in.Approvals.RequestedBy) == "" {
		return fmt.Errorf("promptrun: Approvals.RequestedBy is required")
	}
	if in.OnEvent == nil {
		return fmt.Errorf("promptrun: Approvals requires OnEvent")
	}
	if in.Provider != nil {
		return fmt.Errorf("promptrun: Approvals requires Captain to construct Provider")
	}
	binding, err := in.Config.Approvals()
	if err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	if binding.Func != nil {
		return fmt.Errorf("promptrun: Approvals conflicts with Config.OnApproval (or the deprecated Config.CanUseTool)")
	}
	if requireRecord && in.Record == nil {
		return fmt.Errorf("promptrun: Approvals requires Record")
	}
	if _, err := in.Resolved.Spec.Permissions.ParseApprovalTimeout(); err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	return nil
}

// announceApprovals gives a host's own approval callback the EventPermission the
// broker would otherwise emit: providers no longer announce requests themselves,
// so without this a host answering directly would see its prompts but no event.
// A brokered run is announced by the broker, with its durable approval id.
func announceApprovals(in *Input) {
	if in.Approvals != nil || in.OnEvent == nil {
		return
	}
	in.Config.WrapApprovals(func(next api.ApprovalFunc) api.ApprovalFunc {
		return func(ctx context.Context, req api.ApprovalRequest) (api.ApprovalDecision, error) {
			in.OnEvent(0, api.Event{
				Kind: api.EventPermission, Tool: req.Tool, ToolCallID: req.ToolUseID, Input: req.Input, Request: &req,
			})
			return next(ctx, req)
		}
	})
}

func bindApprovals(ctx context.Context, in *Input, rec *recorder) error {
	if in.Approvals == nil {
		return nil
	}
	if rec == nil {
		return fmt.Errorf("promptrun: Approvals requires an admitted run")
	}
	window, err := in.Resolved.Spec.Permissions.ParseApprovalTimeout()
	if err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	if window == 0 {
		window = approval.ProviderTimeout
	}
	deadline, _ := ctx.Deadline()
	broker := &approval.Broker{
		DB: rec.db, SessionID: rec.sessionID, PromptRunID: rec.runID,
		RequestedBy: in.Approvals.RequestedBy, Timeout: window, Deadline: deadline,
		Notify: func(_ context.Context, event api.Event) error {
			in.OnEvent(0, event)
			return nil
		},
	}
	if err := broker.Validate(); err != nil {
		return fmt.Errorf("promptrun: %w", err)
	}
	in.Config.OnApproval = func(callCtx context.Context, req api.ApprovalRequest) (api.ApprovalDecision, error) {
		approvalCtx, cancel := context.WithCancelCause(callCtx)
		stop := context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
		defer stop()
		defer cancel(nil)
		return broker.OnApproval(approvalCtx, req)
	}
	return nil
}
