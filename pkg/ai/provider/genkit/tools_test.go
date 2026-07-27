package genkit

import (
	"context"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
)

func newToolProvider(canUse api.PermissionFunc) *Provider {
	return &Provider{cfg: ai.Config{Model: api.Model{Name: "test-model"}, CanUseTool: canUse}}
}

func collectEvents() (func(ai.Event), *[]ai.Event) {
	var events []ai.Event
	return func(ev ai.Event) { events = append(events, ev) }, &events
}

func TestRunToolAutoRunsAndEmitsCorrelatedEvents(t *testing.T) {
	p := newToolProvider(nil)
	emit, events := collectEvents()

	var gotInput map[string]any
	def := api.ToolDefinition{
		Name:              "echo",
		DefaultPermission: api.ToolModeOn,
		Handler: func(_ context.Context, in map[string]any) (any, error) {
			gotInput = in
			return map[string]any{"ok": true}, nil
		},
	}

	out, err := runCorrelatedTool(p, def, map[string]any{"x": 1}, emit)
	if err != nil {
		t.Fatalf("runTool: %v", err)
	}
	if gotInput["x"] != 1 {
		t.Errorf("handler input = %v, want {x:1}", gotInput)
	}
	if m, ok := out.(map[string]any); !ok || m["ok"] != true {
		t.Errorf("out = %v, want {ok:true}", out)
	}

	if len(*events) != 2 {
		t.Fatalf("events = %d, want 2 (use, result); got %+v", len(*events), *events)
	}
	use, result := (*events)[0], (*events)[1]
	if use.Kind != ai.EventToolUse || result.Kind != ai.EventToolResult {
		t.Fatalf("kinds = %q,%q, want tool_use,tool_result", use.Kind, result.Kind)
	}
	if use.ToolCallID == "" || use.ToolCallID != result.ToolCallID {
		t.Errorf("call ids not correlated: use=%q result=%q", use.ToolCallID, result.ToolCallID)
	}
	if !result.Success {
		t.Error("result.Success = false, want true")
	}
}

func TestRunToolApprovalDeniedSkipsHandler(t *testing.T) {
	ran := false
	deny := func(_ context.Context, _ api.PermissionRequest) (api.PermissionDecision, error) {
		return api.PermissionDecision{Allow: false, Message: "nope"}, nil
	}
	p := newToolProvider(deny)
	emit, events := collectEvents()

	def := api.ToolDefinition{
		Name:              "danger",
		DefaultPermission: api.ToolModeAsk,
		Handler:           func(context.Context, map[string]any) (any, error) { ran = true; return "ran", nil },
	}

	out, err := runCorrelatedTool(p, def, map[string]any{}, emit)
	if err != nil {
		t.Fatalf("runTool: %v", err)
	}
	if ran {
		t.Error("handler ran despite denial")
	}
	if m, ok := out.(map[string]any); !ok || m["denied"] != true {
		t.Errorf("out = %v, want denied", out)
	}
	kinds := eventKinds(*events)
	if len(kinds) != 3 || kinds[0] != ai.EventToolUse || kinds[1] != ai.EventPermission || kinds[2] != ai.EventToolResult {
		t.Fatalf("event kinds = %v, want [tool_use permission tool_result]", kinds)
	}
	if (*events)[2].Success {
		t.Error("denied result Success = true, want false")
	}
}

func TestRunToolApprovalAllowsAndSubstitutesInput(t *testing.T) {
	allow := func(_ context.Context, _ api.PermissionRequest) (api.PermissionDecision, error) {
		return api.PermissionDecision{Allow: true, UpdatedInput: map[string]any{"amount": 5}}, nil
	}
	p := newToolProvider(allow)
	emit, _ := collectEvents()

	var seen map[string]any
	def := api.ToolDefinition{
		Name:              "pay",
		DefaultPermission: api.ToolModeAsk,
		Handler:           func(_ context.Context, in map[string]any) (any, error) { seen = in; return "done", nil },
	}

	if _, err := runCorrelatedTool(p, def, map[string]any{"amount": 1}, emit); err != nil {
		t.Fatalf("runTool: %v", err)
	}
	if seen["amount"] != 5 {
		t.Errorf("handler input = %v, want the UpdatedInput amount=5", seen)
	}
}

func TestRunToolHandlerErrorFedBack(t *testing.T) {
	p := newToolProvider(nil)
	emit, events := collectEvents()
	def := api.ToolDefinition{
		Name:              "boom",
		DefaultPermission: api.ToolModeOn,
		Handler:           func(context.Context, map[string]any) (any, error) { return nil, context.Canceled },
	}
	out, err := runCorrelatedTool(p, def, map[string]any{}, emit)
	if err != nil {
		t.Fatalf("runTool should not surface handler error, got %v", err)
	}
	if m, ok := out.(map[string]any); !ok || m["error"] == nil {
		t.Errorf("out = %v, want {error:...}", out)
	}
	last := (*events)[len(*events)-1]
	if last.Kind != ai.EventToolResult || last.Success {
		t.Errorf("last event = %+v, want failed tool_result", last)
	}
}

func TestToolOptionsEmptyWhenNoTools(t *testing.T) {
	p := newToolProvider(nil)
	if opts, err := p.toolOptions(nil, nil); err != nil || opts != nil {
		t.Errorf("toolOptions with no tools = %v, want nil", opts)
	}
	if !p.SupportsCallerTools() {
		t.Error("SupportsCallerTools = false, want true")
	}
}

func eventKinds(events []ai.Event) []ai.EventKind {
	kinds := make([]ai.EventKind, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	return kinds
}

func runCorrelatedTool(p *Provider, def api.ToolDefinition, input any, emit func(ai.Event)) (any, error) {
	correlation := newToolEventCorrelation()
	correlation.observeRequest(&gkai.ToolRequest{Name: def.Name, Ref: "provider-call-1", Input: input})
	return p.runTool(context.Background(), def, input, emit, correlation)
}
