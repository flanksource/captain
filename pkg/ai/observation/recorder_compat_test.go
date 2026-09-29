package observation

import (
	"context"
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestDeprecatedPermissionBrokerRunsInLegacyMode(t *testing.T) {
	var calls int
	approve := func(context.Context, api.PermissionRequest) (api.PermissionDecision, error) {
		calls++
		return api.PermissionDecision{Allow: true}, nil
	}
	broker := NewRecorder().PermissionBroker(approve)

	legacy := api.ApprovalRequest{Tool: "Edit", Kind: api.ApprovalKindTool, ToolUseID: "toolu_1", LegacyContract: true}
	if decision, err := broker(context.Background(), legacy); err != nil || !decision.Allow {
		t.Fatalf("legacy tool request = (%+v, %v), want an allow from the wrapped callback", decision, err)
	}
	command := api.ApprovalRequest{Tool: "exec_command", Kind: api.ApprovalKindCommand, ToolUseID: "appr_1",
		Command: &api.CommandApproval{Command: "git push"}}
	if _, err := broker(context.Background(), command); !errors.Is(err, api.ErrLegacyApprovalSkipped) {
		t.Fatalf("command request error = %v, want ErrLegacyApprovalSkipped", err)
	}
	if calls != 1 {
		t.Fatalf("wrapped callback called %d times, want 1 (only the legacy request)", calls)
	}
}
