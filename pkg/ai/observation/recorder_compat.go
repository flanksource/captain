package observation

import "github.com/flanksource/captain/pkg/api"

// PermissionBroker is the pre-rename ApprovalBroker. The callback it returns runs
// in legacy mode.
//
// COMPAT(unified-approval): remove in Phase 6.
//
// Deprecated: use ApprovalBroker. Removed in the unified-approval Phase 6.
func (r *Recorder) PermissionBroker(next api.PermissionFunc) api.PermissionFunc {
	return api.LegacyApprovalFunc(r.ApprovalBroker(next), "Recorder.PermissionBroker", api.CallerLocation(1))
}
