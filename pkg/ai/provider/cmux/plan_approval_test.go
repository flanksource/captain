package cmux

import (
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

const planCallLine = `{"type":"assistant","sessionId":"s","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_plan","name":"ExitPlanMode","input":{"plan":"1. Inspect\n2. Implement","planFilePath":"/repo/.claude/plans/example.md"}}]}}`

var loggedPlan = &pendingPlan{
	toolUseID: "toolu_plan",
	plan:      &api.TerminalPlan{Content: "1. Inspect\n2. Implement", Path: "/repo/.claude/plans/example.md"},
}

func TestSessionAccumulatorHandsOutEachPlanCallOnce(t *testing.T) {
	acc := &SessionAccumulator{}
	if plan := acc.takePlan(); plan != nil {
		t.Fatalf("takePlan() before any ExitPlanMode = %+v, want nil", plan)
	}
	acc.AddLine([]byte(planCallLine))
	if got := acc.takePlan(); !reflect.DeepEqual(got, loggedPlan) {
		t.Fatalf("takePlan() = %+v, want %+v", got, loggedPlan)
	}
	if again := acc.takePlan(); again != nil {
		t.Fatalf("takePlan() replayed an answered plan call: %+v", again)
	}
	if state := acc.state(); state != sessionStateAsk {
		t.Fatalf("state() = %q, want %q", state, sessionStateAsk)
	}
}

func TestPlanDialogWaitsForThePlanInTheSessionLog(t *testing.T) {
	if _, ok := detectApprovalRequest("sess", planApprovalScreen, nil); ok {
		t.Fatal("detectApprovalRequest raised a plan approval before the plan reached the session log")
	}
}

func TestPlanDialogIsAPlanApproval(t *testing.T) {
	req, ok := detectApprovalRequest("sess", planApprovalScreen, loggedPlan)
	if !ok {
		t.Fatal("detectApprovalRequest missed the plan dialog")
	}
	want := parsePlanApprovalRequest("sess", *loggedPlan)
	if !reflect.DeepEqual(req, want) {
		t.Fatalf("request = %+v, want %+v", req, want)
	}
	if req.Kind != api.ApprovalKindPlan || req.ToolUseID != "toolu_plan" || req.Plan != loggedPlan.plan {
		t.Fatalf("request = %+v, want a plan approval keyed by the logged tool_use id", req)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}
