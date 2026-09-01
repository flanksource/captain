package provider

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

func TestBuildGeminiCLIArgs(t *testing.T) {
	args, err := buildGeminiCLIArgs("gemini-3.5-flash", ai.Request{Prompt: api.Prompt{User: "hi"}})
	if err != nil {
		t.Fatalf("buildGeminiCLIArgs: %v", err)
	}
	// stream-json is what makes the run parseable; without it gemini prints prose.
	requireFlagValue(t, args, "--output-format", "stream-json")
	requireFlagValue(t, args, "--model", "gemini-3.5-flash")
	for _, arg := range args {
		if arg == "--approval-mode" {
			t.Fatalf("default posture must not pin an approval mode, got %v", args)
		}
	}
}

func TestGeminiCLIApprovalMode(t *testing.T) {
	cases := []struct {
		name string
		mode api.PermissionMode
		want string
	}{
		{"default posture leaves gemini's own default", "", ""},
		{"explicit default posture", api.PermissionDefault, ""},
		{"plan is gemini's read-only mode", api.PermissionPlan, "plan"},
		{"acceptEdits auto-approves edit tools", api.PermissionAcceptEdits, "auto_edit"},
		{"auto auto-approves edit tools", api.PermissionAuto, "auto_edit"},
		{"bypass is yolo", api.PermissionBypass, "yolo"},
		{"dontAsk is yolo", api.PermissionDontAsk, "yolo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := geminiApprovalMode(tc.mode); got != tc.want {
				t.Fatalf("geminiApprovalMode = %q, want %q", got, tc.want)
			}
			args, err := buildGeminiCLIArgs("gemini-3.5-flash", ai.Request{
				Sandbox:     &api.SandboxRef{Mode: api.SandboxDocker},
				Permissions: api.Permissions{Mode: tc.mode},
			})
			if err != nil {
				t.Fatalf("buildGeminiCLIArgs: %v", err)
			}
			if tc.want != "" {
				requireFlagValue(t, args, "--approval-mode", tc.want)
			}
		})
	}
}

func TestBuildGeminiCLIArgsRejectsAttachments(t *testing.T) {
	// The registry declares no media types for gemini-cli, so an attachment must
	// fail loudly rather than being silently dropped from the prompt.
	req := ai.Request{Prompt: api.Prompt{
		User:        "describe this",
		Attachments: []api.AttachmentRef{{ID: "shot", MediaType: "image/png"}},
	}}
	if _, err := buildGeminiCLIArgs("gemini-3.5-flash", req); err == nil {
		t.Fatal("attachment on gemini-cli must be rejected")
	}
}

// The wire format below is gemini CLI 0.50's `--output-format stream-json`
// (StreamJsonFormatter): one JSON object per line, assistant text arriving as
// deltas and a single terminal `result` carrying session stats.
func TestGeminiCLIStateMapsStreamJSON(t *testing.T) {
	state := &geminiCLIState{model: "gemini-3.5-flash"}

	init := state.mapLine([]byte(`{"type":"init","timestamp":"2026-07-26T00:00:00.000Z","session_id":"sess-1","model":"gemini-3.5-flash"}`))
	if len(init) != 1 || init[0].Kind != ai.EventSystem || init[0].SessionID != "sess-1" {
		t.Fatalf("init events = %+v, want one EventSystem carrying the session id", init)
	}

	// The user echo is the prompt coming back; emitting it would double-count the
	// input as assistant text in the coalesced response.
	if echo := state.mapLine([]byte(`{"type":"message","role":"user","content":"hi"}`)); len(echo) != 0 {
		t.Fatalf("user echo produced events: %+v", echo)
	}

	text := state.mapLine([]byte(`{"type":"message","role":"assistant","content":"Hello ","delta":true}`))
	if len(text) != 1 || text[0].Kind != ai.EventText || text[0].Text != "Hello " {
		t.Fatalf("assistant delta events = %+v, want one EventText", text)
	}
	if text[0].SessionID != "sess-1" {
		t.Fatalf("assistant delta lost the session id: %+v", text[0])
	}

	use := state.mapLine([]byte(`{"type":"tool_use","tool_name":"read_file","tool_id":"call-1","parameters":{"absolute_path":"/tmp/x"}}`))
	if len(use) != 1 || use[0].Kind != ai.EventToolUse || use[0].Tool != "read_file" || use[0].ToolCallID != "call-1" {
		t.Fatalf("tool_use events = %+v", use)
	}
	if got, _ := use[0].Input["absolute_path"].(string); got != "/tmp/x" {
		t.Fatalf("tool_use input = %+v, want absolute_path=/tmp/x", use[0].Input)
	}

	ok := state.mapLine([]byte(`{"type":"tool_result","tool_id":"call-1","status":"success","output":"file body"}`))
	if len(ok) != 1 || ok[0].Kind != ai.EventToolResult || !ok[0].Success || ok[0].Text != "file body" {
		t.Fatalf("tool_result events = %+v", ok)
	}

	failed := state.mapLine([]byte(`{"type":"tool_result","tool_id":"call-2","status":"error","output":"","error":{"type":"TOOL_EXECUTION_ERROR","message":"no such file"}}`))
	if len(failed) != 1 || failed[0].Success || failed[0].Text != "no such file" {
		t.Fatalf("errored tool_result events = %+v", failed)
	}

	// A warning-severity error is advisory: it must not be swallowed, but the run
	// still ends on the terminal result.
	warn := state.mapLine([]byte(`{"type":"error","severity":"warning","message":"rate limited, retrying"}`))
	if len(warn) != 1 || warn[0].Kind != ai.EventError || warn[0].Error != "rate limited, retrying" {
		t.Fatalf("warning events = %+v", warn)
	}

	done := state.mapLine([]byte(`{"type":"result","status":"success","stats":{"total_tokens":150,"input_tokens":120,"output_tokens":30,"cached":100,"input":20,"duration_ms":900,"tool_calls":1}}`))
	if len(done) != 1 || done[0].Kind != ai.EventResult || !done[0].Success {
		t.Fatalf("result events = %+v", done)
	}
	usage := done[0].Usage
	if usage == nil {
		t.Fatal("terminal result carried no usage")
	}
	// gemini's input_tokens is gross (cache included); captain's buckets are
	// disjoint, so the cached prefix must be netted out of InputTokens.
	if usage.InputTokens != 20 || usage.CacheReadTokens != 100 || usage.OutputTokens != 30 {
		t.Fatalf("usage = %+v, want input=20 cacheRead=100 output=30", *usage)
	}
	if usage.InputTokens+usage.CacheReadTokens != 120 {
		t.Fatalf("netted input buckets = %d, want the reported gross 120", usage.InputTokens+usage.CacheReadTokens)
	}
	if usage.TotalTokens() != 150 {
		t.Fatalf("total tokens = %d, want the reported 150", usage.TotalTokens())
	}
}

func TestGeminiCLIStateMapsFailedResult(t *testing.T) {
	state := &geminiCLIState{model: "gemini-3.5-flash"}
	events := state.mapLine([]byte(`{"type":"result","status":"error","error":{"type":"IneligibleTierError","message":"account not eligible"},"stats":{"input_tokens":5,"output_tokens":0}}`))
	if len(events) != 1 || events[0].Kind != ai.EventResult {
		t.Fatalf("events = %+v, want one EventResult", events)
	}
	if events[0].Success || events[0].Error != "account not eligible" {
		t.Fatalf("failed result = %+v, want Success=false carrying the error message", events[0])
	}
}

func TestGeminiCLIStateValidatesPromptAppendedSchema(t *testing.T) {
	schema := []byte(`{"type":"object","required":["pass"],"properties":{"pass":{"type":"boolean"}}}`)

	state := &geminiCLIState{model: "gemini-3.5-flash", schema: schema}
	state.mapLine([]byte(`{"type":"message","role":"assistant","content":"{\"pass\":true}","delta":true}`))
	events := state.mapLine([]byte(`{"type":"result","status":"success","stats":{"input_tokens":1,"output_tokens":1}}`))
	if len(events) != 1 || !events[0].Success {
		t.Fatalf("events = %+v, want one successful EventResult", events)
	}
	if string(events[0].StructuredData) != `{"pass":true}` {
		t.Fatalf("structured data = %s", events[0].StructuredData)
	}

	// A reply that ignores the schema must fail the run, not return empty output.
	bad := &geminiCLIState{model: "gemini-3.5-flash", schema: schema}
	bad.mapLine([]byte(`{"type":"message","role":"assistant","content":"sure thing!","delta":true}`))
	events = bad.mapLine([]byte(`{"type":"result","status":"success","stats":{}}`))
	if len(events) != 2 || events[0].Kind != ai.EventError || events[1].Kind != ai.EventResult || events[1].Success {
		t.Fatalf("events = %+v, want an EventError then a failed EventResult", events)
	}
	if !strings.Contains(events[1].Error, "no JSON object") {
		t.Fatalf("failed result error = %q, want the schema-extraction failure", events[1].Error)
	}
}

func TestGeminiCLIStateReportsUnparseableLine(t *testing.T) {
	state := &geminiCLIState{model: "gemini-3.5-flash"}
	events := state.mapLine([]byte(`not json`))
	if len(events) != 1 || events[0].Kind != ai.EventError {
		t.Fatalf("events = %+v, want one EventError", events)
	}
}
