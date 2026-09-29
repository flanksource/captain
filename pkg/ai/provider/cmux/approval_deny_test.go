package cmux

import (
	"context"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

func TestHandleApprovalRefusesDeniedToolWithoutAsking(t *testing.T) {
	runner := &stallRunner{}
	r, events := recordingRun(runConfig{}, runner.run)
	r.toolPolicy = aitools.ResolveOptions{Preferences: api.ToolPreferences{"Bash": api.ToolPolicyDeny}}
	var asked int
	r.onApproval = func(context.Context, ai.ApprovalRequest) (ai.ApprovalDecision, error) {
		asked++
		return ai.ApprovalDecision{Allow: true}, nil
	}
	req := ai.ApprovalRequest{SessionID: "denied", Tool: "Bash", Kind: api.ApprovalKindTool, LegacyContract: true,
		Input: map[string]any{"prompt": "Do you want to run this command?"}}

	r.handleApproval(context.Background(), testSurface, req)

	if asked != 0 {
		t.Fatalf("callback asked %d times about a denied tool, want 0", asked)
	}
	if runner.escapeCount() != 1 || runner.enterCount() != 0 {
		t.Fatalf("keys sent: escape=%d enter=%d, want escape=1 enter=0", runner.escapeCount(), runner.enterCount())
	}
	if hasPermissionEvent(*events, "Bash") {
		t.Fatalf("EventPermission emitted for a denied tool: %v", *events)
	}
}

func TestHandleApprovalDismissesDialogWhenNobodyAnswers(t *testing.T) {
	runner := &stallRunner{}
	r, _ := recordingRun(runConfig{}, runner.run)
	r.onApproval = func(context.Context, ai.ApprovalRequest) (ai.ApprovalDecision, error) {
		return ai.ApprovalDecision{}, context.DeadlineExceeded
	}
	req := ai.ApprovalRequest{SessionID: "expired", Tool: "Edit", Kind: api.ApprovalKindTool, LegacyContract: true,
		Input: map[string]any{"prompt": "Do you want to make this edit?"}}

	r.handleApproval(context.Background(), testSurface, req)

	if runner.escapeCount() != 1 || runner.enterCount() != 0 {
		t.Fatalf("keys sent: escape=%d enter=%d, want the dialog dismissed (escape=1 enter=0)", runner.escapeCount(), runner.enterCount())
	}
}

func TestApprovalToolUseIDSeparatesRepeatedDialogs(t *testing.T) {
	req := ai.ApprovalRequest{Tool: "Bash", Input: map[string]any{"prompt": "Do you want to run this command?"}}
	first := approvalToolUseID(testSurface, 1, req)
	second := approvalToolUseID(testSurface, 2, req)
	if first == second {
		t.Fatalf("two dialogs with the same text share id %q; the second would replay the first answer", first)
	}
	if first != approvalToolUseID(testSurface, 1, req) {
		t.Fatalf("the same dialog must keep its id across retries")
	}
	if !strings.HasPrefix(first, testSurface.String()+":1:") {
		t.Fatalf("id %q does not start with the surface and sequence", first)
	}
}
