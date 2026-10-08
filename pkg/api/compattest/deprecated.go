//go:build unified_approval_compat

// Package compattest uses every identifier the unified approval callback
// deprecated, the way a pre-rename caller such as Gavel v0.0.63 does. It is
// built only under its tag, so the deprecation lint never sees it, and
// compattest_ginkgo_test.go fails when a deprecated name stops compiling.
//
// COMPAT(unified-approval): deleted in Phase 6 together with the aliases.
package compattest

import (
	"context"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
)

var approveAll api.PermissionFunc = func(context.Context, api.PermissionRequest) (api.PermissionDecision, error) {
	return api.PermissionDecision{Allow: true}, nil
}

var _ ai.PermissionFunc = func(context.Context, ai.PermissionRequest) (ai.PermissionDecision, error) {
	return ai.PermissionDecision{}, nil
}

var _ = api.Config{CanUseTool: approveAll}

var _ = callertools.Options{CanUseTool: approveAll}

func recorded(recorder *observation.Recorder) api.PermissionFunc {
	return recorder.PermissionBroker(approveAll)
}

func brokered(ctx context.Context, broker *approval.Broker) (api.PermissionDecision, error) {
	return broker.CanUseTool(ctx, api.PermissionRequest{Tool: "Edit", ToolUseID: "toolu_1"})
}

var _, _ = recorded, brokered
