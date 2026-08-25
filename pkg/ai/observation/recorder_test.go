package observation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestRecorderCapturesNativeEffortWithoutConfigurationFallback(t *testing.T) {
	recorder := NewRecorder()
	ctx := ContextWithRecorder(context.Background(), recorder)
	RecordReasoningDispatch(ctx, "openai.responses.create", true, "high")

	snapshot := recorder.Snapshot()
	if snapshot.Effort.State != api.ObservationFactKnown || snapshot.Effort.Value == nil || *snapshot.Effort.Value != "high" {
		t.Fatalf("observed effort = %#v, want known high", snapshot.Effort)
	}
	if len(snapshot.Effort.EvidenceRefs) != 1 || snapshot.Effort.EvidenceRefs[0] != "dispatch-1" {
		t.Fatalf("effort evidence = %v, want dispatch-1", snapshot.Effort.EvidenceRefs)
	}

	unset := NewRecorder()
	RecordReasoningDispatch(ContextWithRecorder(context.Background(), unset), "codex.exec.start", false, "")
	if got := unset.Snapshot().Effort; got.State != api.ObservationFactUnset {
		t.Fatalf("omitted native effort = %#v, want unset", got)
	}

	missing := NewRecorder().Snapshot().Effort
	if missing.State != api.ObservationFactUnknown {
		t.Fatalf("missing instrumentation = %#v, want unknown", missing)
	}
}

func TestRecorderCorrelatesDeniedPermissionWithUnstartedTool(t *testing.T) {
	recorder := NewRecorder()
	recorder.RecordEvent(api.Event{
		Kind: api.EventToolUse, Tool: "sentinel.write", ToolCallID: "call-1",
		Input: map[string]any{"secret": "must-not-appear"},
	})
	decision, err := recorder.PermissionBroker(nil)(context.Background(), api.PermissionRequest{
		Tool: "sentinel.write", ToolUseID: "call-1", Input: map[string]any{"secret": "must-not-appear"},
	})
	if err != nil || decision.Allow {
		t.Fatalf("default broker decision = %#v, %v; want deny", decision, err)
	}
	recorder.RecordEvent(api.Event{
		Kind: api.EventToolResult, Tool: "sentinel.write", ToolCallID: "call-1", Success: false,
		Text: "must-not-appear",
	})

	snapshot := recorder.Snapshot()
	if len(snapshot.Permissions) != 1 || snapshot.Permissions[0].Decision != "denied" || snapshot.Permissions[0].ToolCallID != "call-1" {
		t.Fatalf("permission events = %#v", snapshot.Permissions)
	}
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Execution.State != "not_started" || snapshot.Tools[0].ToolCallID != "call-1" {
		t.Fatalf("tool events = %#v", snapshot.Tools)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(data), "must-not-appear") {
		t.Fatalf("recorder retained provider content: %s", data)
	}
}
