package approval

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
)

// CanUseTool is the pre-rename OnApproval. Callers of it predate Kind, so a
// request without one is the only shape they could send: a tool approval.
//
// COMPAT(unified-approval): remove in Phase 6.
//
// Deprecated: use OnApproval. Removed in the unified-approval Phase 6.
func (b *Broker) CanUseTool(ctx context.Context, req api.ApprovalRequest) (api.ApprovalDecision, error) {
	api.WarnDeprecatedEntryPoint("Broker.CanUseTool", api.CallerLocation(1))
	if req.Kind == "" {
		req.Kind = api.ApprovalKindTool
	}
	return b.OnApproval(ctx, req)
}
