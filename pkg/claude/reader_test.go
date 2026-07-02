package claude

import (
	"strings"
	"testing"
)

func TestReadHistory(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"Hello"}]}}
{"uuid":"2","sessionId":"s1","timestamp":"2024-01-15T10:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Hi there!"}]}}
{"uuid":"3","sessionId":"s1","timestamp":"2024-01-15T10:02:00Z","message":{"role":"user","content":[{"type":"text","text":"Bye"}]}}`

	entries, err := ReadHistory(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ReadHistory failed: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].UUID != "1" || entries[0].Message.Role != MessageRoleUser {
		t.Errorf("unexpected first entry: %+v", entries[0])
	}

	if entries[1].UUID != "2" || entries[1].Message.Role != MessageRoleAssistant {
		t.Errorf("unexpected second entry: %+v", entries[1])
	}
}

func TestReadHistoryWithOptionsCanSkipRawLines(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"pwd"}}]}}`

	withRaw, err := ReadHistory(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ReadHistory failed: %v", err)
	}
	if len(withRaw) != 1 || len(withRaw[0].RawLine) == 0 {
		t.Fatalf("ReadHistory should keep raw lines by default, got %+v", withRaw)
	}

	withoutRaw, err := ReadHistoryWithOptions(strings.NewReader(jsonl), ReadOptions{})
	if err != nil {
		t.Fatalf("ReadHistoryWithOptions failed: %v", err)
	}
	if len(withoutRaw) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(withoutRaw))
	}
	if len(withoutRaw[0].RawLine) != 0 {
		t.Fatalf("ReadHistoryWithOptions without KeepRaw should omit raw line, got %q", string(withoutRaw[0].RawLine))
	}

	uses := ExtractToolUses(withoutRaw)
	if len(uses) != 1 || len(uses[0].RawEntry) != 0 {
		t.Fatalf("tool use should not carry raw entry when KeepRaw is false: %+v", uses)
	}
}

func TestReadHistory_EmptyLines(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[]}}

{"uuid":"2","sessionId":"s1","timestamp":"2024-01-15T10:01:00Z","message":{"role":"assistant","content":[]}}
`

	entries, err := ReadHistory(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ReadHistory failed: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("expected 2 entries (skipping empty lines), got %d", len(entries))
	}
}

func TestReadHistory_InvalidJSON(t *testing.T) {
	// Bad lines now surface as ParseError synthetic rows rather than
	// short-circuiting the read. Subsequent good lines are still parsed.
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[]}}
not valid json
{"uuid":"2","sessionId":"s1","timestamp":"2024-01-15T10:01:00Z","message":{"role":"assistant","content":[]}}`

	entries, err := ReadHistory(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ReadHistory should not fail; bad lines surface as ParseError rows: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (user, ParseError, assistant), got %d", len(entries))
	}
	parseErr := entries[1].Message.GetToolUses()
	if len(parseErr) != 1 || parseErr[0].Name != "ParseError" {
		t.Errorf("entry[1] expected ParseError, got %+v", parseErr)
	}
	if entries[2].UUID != "2" {
		t.Errorf("entry[2] should be the post-bad-line assistant message, got UUID=%s", entries[2].UUID)
	}
}

func TestReadHistory_SlugAndPlanModeAttachment(t *testing.T) {
	// Session-file lines carry a `type`, so they route through dispatchEvent.
	// The plan_mode_exit attachment surfaces as a message-less entry holding the
	// plan path; the non-plan attachment is dropped.
	jsonl := `{"type":"assistant","sessionId":"s1","uuid":"a1","timestamp":"2026-06-01T10:00:00Z","slug":"keen-otter","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}
{"type":"attachment","sessionId":"s1","uuid":"at1","timestamp":"2026-06-01T10:00:01Z","slug":"keen-otter","cwd":"/repo","attachment":{"type":"plan_mode_exit","planFilePath":"/home/u/.claude/plans/keen-otter.md","planExists":true}}
{"type":"attachment","sessionId":"s1","uuid":"at2","attachment":{"type":"file_edit","path":"/x"}}`

	entries, err := ReadHistory(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ReadHistory failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (assistant + plan attachment), got %d", len(entries))
	}
	if entries[0].Slug != "keen-otter" {
		t.Errorf("assistant slug = %q, want keen-otter", entries[0].Slug)
	}
	plan := entries[1]
	if plan.PlanFilePath != "/home/u/.claude/plans/keen-otter.md" {
		t.Errorf("attachment PlanFilePath = %q", plan.PlanFilePath)
	}
	if plan.Slug != "keen-otter" || plan.CWD != "/repo" {
		t.Errorf("attachment slug/cwd = %q/%q", plan.Slug, plan.CWD)
	}
	if len(plan.Message.Content) != 0 {
		t.Errorf("plan attachment entry should be message-less, got %+v", plan.Message.Content)
	}
}

func TestReadStreamJSON(t *testing.T) {
	input := `{"type":"system","subtype":"init","cwd":"/tmp","session_id":"sess-1","uuid":"u-init","model":"claude-sonnet-4-20250514","tools":["Bash","Read"]}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"list files"}]},"session_id":"sess-1","uuid":"msg-1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll list the files."},{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"ls -la"}}]},"session_id":"sess-1","uuid":"msg-2","timestamp":"2024-01-15T10:00:00Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-2","name":"Read","input":{"file_path":"/tmp/foo.go"}}]},"session_id":"sess-1","uuid":"msg-3","timestamp":"2024-01-15T10:01:00Z"}
{"type":"result","subtype":"success","session_id":"sess-1","uuid":"u-result","total_cost_usd":0.01,"duration_ms":500,"num_turns":2,"result":"Done."}`

	ResetUnhandledStreamTypes()
	entries, err := ReadStreamJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ReadStreamJSON failed: %v", err)
	}

	// Entries: system/init (synthetic), user, assistant, assistant, result (synthetic) = 5
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries (init, user, 2x assistant, result), got %d", len(entries))
	}

	// First entry is the synthesized system/init
	initTU := entries[0].Message.GetToolUses()
	if len(initTU) != 1 || initTU[0].Name != "SessionInit" {
		t.Errorf("expected SessionInit synthetic tool use, got %+v", initTU)
	}

	// Real assistant entries with native tool_use blocks
	bashTU := entries[2].Message.GetToolUses()
	if len(bashTU) != 1 || bashTU[0].Name != "Bash" {
		t.Errorf("expected Bash tool use in entry 2, got %+v", bashTU)
	}

	readTU := entries[3].Message.GetToolUses()
	if len(readTU) != 1 || readTU[0].Name != "Read" {
		t.Errorf("expected Read tool use in entry 3, got %+v", readTU)
	}

	// Last entry is the synthesized result
	resultTU := entries[4].Message.GetToolUses()
	if len(resultTU) != 1 || resultTU[0].Name != "Result" {
		t.Errorf("expected Result synthetic tool use, got %+v", resultTU)
	}

	if got := SnapshotUnhandledStreamTypes(); len(got) != 0 {
		t.Errorf("expected no unhandled types, got %v", got)
	}
}

func TestReadStreamJSON_HookEvents(t *testing.T) {
	input := `{"type":"system","subtype":"hook_started","hook_id":"h1","hook_name":"SessionStart:startup","hook_event":"SessionStart","session_id":"s","uuid":"u1"}
{"type":"system","subtype":"hook_response","hook_id":"h1","hook_name":"SessionStart:startup","outcome":"success","exit_code":0,"stdout":"OK\n","stderr":"","session_id":"s","uuid":"u2"}`

	entries, err := ReadStreamJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ReadStreamJSON failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 hook entries, got %d", len(entries))
	}
	if name := entries[0].Message.GetToolUses()[0].Name; name != "HookStart" {
		t.Errorf("expected HookStart, got %s", name)
	}
	if name := entries[1].Message.GetToolUses()[0].Name; name != "HookResponse" {
		t.Errorf("expected HookResponse, got %s", name)
	}
}

func TestReadStreamJSON_UnhandledTypes(t *testing.T) {
	ResetUnhandledStreamTypes()
	input := `{"type":"file-history-snapshot","messageId":"x","snapshot":{}}
{"type":"agent-name","name":"foo"}
{"type":"completely-novel-type","x":1}
{"type":"system","subtype":"init","cwd":"/tmp","session_id":"s","uuid":"u"}`

	entries, err := ReadStreamJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ReadStreamJSON failed: %v", err)
	}
	// system/init produces an entry. file-history-snapshot and agent-name are
	// known session-storage types (recognized, not surfaced, NOT counted).
	// Only completely-novel-type is unknown and counted.
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	snap := SnapshotUnhandledStreamTypes()
	if snap["completely-novel-type"] != 1 {
		t.Errorf("expected completely-novel-type=1, got %d", snap["completely-novel-type"])
	}
	if _, ok := snap["file-history-snapshot"]; ok {
		t.Errorf("file-history-snapshot is a known-but-skipped type, should NOT be in unhandled snapshot, got %v", snap)
	}
	if _, ok := snap["agent-name"]; ok {
		t.Errorf("agent-name is a known-but-skipped type, should NOT be in unhandled snapshot, got %v", snap)
	}
}

func TestReadHistory_SessionFileEvents(t *testing.T) {
	// On-disk session files use camelCase fields and a different mix of
	// types from stream-json. ReadHistory must recognize the same set of
	// events ReadStreamJSON does.
	jsonl := `{"sessionId":"s","uuid":"1","timestamp":"2024-01-01T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"type":"ai-title","aiTitle":"My Session","sessionId":"s"}
{"type":"system","subtype":"turn_duration","durationMs":1234,"messageCount":5,"sessionId":"s","uuid":"td","timestamp":"2024-01-01T10:01:00Z"}
{"type":"system","subtype":"stop_hook_summary","hookCount":3,"hookErrors":0,"stopReason":"end_turn","sessionId":"s","uuid":"sh","timestamp":"2024-01-01T10:02:00Z"}
{"type":"system","subtype":"away_summary","content":"User stepped away","sessionId":"s","uuid":"as"}
{"type":"agent-name","agentName":"foo","sessionId":"s"}
{"type":"file-history-snapshot","messageId":"m","snapshot":{}}
{"type":"permission-mode","permissionMode":"plan","sessionId":"s"}
{"type":"attachment","attachment":"x","sessionId":"s","uuid":"a"}`

	ResetUnhandledStreamTypes()
	entries, err := ReadHistory(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ReadHistory failed: %v", err)
	}

	// Entries: user message + 4 surfaced events (title, turn_duration,
	// stop_hook_summary, away_summary). Storage types are silently dropped.
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries (user + 4 events), got %d:\n%+v", len(entries), entries)
	}

	wantNames := []string{"", "SessionTitle", "TurnDuration", "StopHookSummary", "AwaySummary"}
	for i, want := range wantNames {
		if want == "" {
			// First entry is a real user message — no synthetic tool name.
			if entries[i].Message.Role != MessageRoleUser {
				t.Errorf("entry[0] expected user role, got %s", entries[i].Message.Role)
			}
			continue
		}
		uses := entries[i].Message.GetToolUses()
		if len(uses) != 1 || uses[0].Name != want {
			t.Errorf("entry[%d] expected synthetic tool %q, got %+v", i, want, uses)
		}
	}

	if got := SnapshotUnhandledStreamTypes(); len(got) != 0 {
		t.Errorf("session-storage types should be known-but-skipped, got unhandled: %v", got)
	}
}

func TestReadStreamJSON_AssistantErrorEmitsApiError(t *testing.T) {
	// An assistant line carrying a top-level "error" field must surface as
	// both the assistant message AND a separate ApiError synthetic row,
	// otherwise the failure would be invisible in history output.
	input := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"oops"}]},"session_id":"s","uuid":"u","error":"invalid_request","api_error_status":404}`

	entries, err := ReadStreamJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ReadStreamJSON failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (assistant + ApiError), got %d", len(entries))
	}
	if entries[0].Message.Role != MessageRoleAssistant {
		t.Errorf("entry[0] expected assistant role, got %s", entries[0].Message.Role)
	}
	uses := entries[1].Message.GetToolUses()
	if len(uses) != 1 || uses[0].Name != "ApiError" {
		t.Errorf("entry[1] expected ApiError synthetic tool, got %+v", uses)
	}
}

func TestReadStreamJSON_ParseErrorIsSurfaced(t *testing.T) {
	// Lines that fail to unmarshal must produce ParseError rows rather than
	// being silently dropped (CW-2).
	input := `{"type":"system","subtype":"init","cwd":"/tmp","session_id":"s","uuid":"u"}
not valid json
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]},"session_id":"s","uuid":"u2"}`

	entries, err := ReadStreamJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ReadStreamJSON failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (init, ParseError, assistant), got %d", len(entries))
	}

	parseErr := entries[1].Message.GetToolUses()
	if len(parseErr) != 1 || parseErr[0].Name != "ParseError" {
		t.Errorf("entry[1] expected ParseError, got %+v", parseErr)
	}

	// The assistant message after the bad line should still be parsed.
	if entries[2].UUID != "u2" {
		t.Errorf("entry[2] expected to be assistant (uuid=u2), got UUID=%s", entries[2].UUID)
	}
}

func TestReadStreamJSON_EmptyInput(t *testing.T) {
	entries, err := ReadStreamJSON(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestHistoryIterator(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"First"}]}}
{"uuid":"2","sessionId":"s1","timestamp":"2024-01-15T10:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Second"}]}}`

	it := NewHistoryIterator(strings.NewReader(jsonl))

	var entries []HistoryEntry
	for it.Next() {
		entries = append(entries, it.Entry())
	}

	if it.Err() != nil {
		t.Fatalf("iterator error: %v", it.Err())
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Message.GetTextContent() != "First" {
		t.Errorf("unexpected first text: %q", entries[0].Message.GetTextContent())
	}

	if entries[1].Message.GetTextContent() != "Second" {
		t.Errorf("unexpected second text: %q", entries[1].Message.GetTextContent())
	}
}

func TestHistoryIterator_Error(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[]}}
invalid json here`

	it := NewHistoryIterator(strings.NewReader(jsonl))

	if !it.Next() {
		t.Fatal("expected first Next() to succeed")
	}

	if it.Next() {
		t.Error("expected second Next() to fail due to invalid JSON")
	}

	if it.Err() == nil {
		t.Error("expected Err() to return error")
	}
}
