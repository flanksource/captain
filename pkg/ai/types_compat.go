package ai

import "github.com/flanksource/captain/pkg/api"

// COMPAT(unified-approval): removed in Phase 6 of
// docs/plans/unified-approval-callback.md.
type (
	// Deprecated: use ApprovalFunc. Removed in the unified-approval Phase 6.
	PermissionFunc = api.ApprovalFunc
	// Deprecated: use ApprovalRequest. Removed in the unified-approval Phase 6.
	PermissionRequest = api.ApprovalRequest
	// Deprecated: use ApprovalDecision. Removed in the unified-approval Phase 6.
	PermissionDecision = api.ApprovalDecision
)
