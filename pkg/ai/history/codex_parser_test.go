package history

import (
	"os"
	"strings"
	"testing"
)

func TestExtractCodexToolUses_LiveDottedSchema(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"019e0365-dc2a-7ad0-a5a8-78936481a928"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"message","role":"assistant","text":"hello world"}}`,
		`{"type":"error","message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"The 'gpt-5.5-codex' model is not supported when using Codex with a ChatGPT account.\"}}"}`,
		`{"type":"turn.failed","error":{"message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"The 'gpt-5.5-codex' model is not supported when using Codex with a ChatGPT account.\"}}"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":42}}`,
	}, "\n")

	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}

	if len(uses) != 3 {
		t.Fatalf("expected 3 tool uses (1 message + 2 errors), got %d: %+v", len(uses), uses)
	}

	if uses[0].Tool != "Assistant" || uses[0].Input["text"] != "hello world" {
		t.Errorf("uses[0] = %+v, want Assistant 'hello world'", uses[0])
	}

	wantErr := "The 'gpt-5.5-codex' model is not supported when using Codex with a ChatGPT account."
	for i, u := range uses[1:] {
		if u.Tool != "ApiError" {
			t.Errorf("uses[%d] tool = %q, want ApiError", i+1, u.Tool)
		}
		if got := u.Input["error"]; got != wantErr {
			t.Errorf("uses[%d] error = %q\n want %q", i+1, got, wantErr)
		}
	}

	for _, u := range uses {
		if u.SessionID != "019e0365-dc2a-7ad0-a5a8-78936481a928" {
			t.Errorf("expected session id propagated from thread.started, got %q on %s", u.SessionID, u.Tool)
		}
	}
}

func TestExtractCodexToolUses_LiveItemContentArray(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"sess-x"}`,
		`{"type":"item.completed","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"part-a"},{"type":"output_text","text":"part-b"}]}}`,
	}, "\n")

	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}
	if len(uses) != 1 {
		t.Fatalf("expected 1 tool use, got %d", len(uses))
	}
	if uses[0].Input["text"] != "part-apart-b" {
		t.Errorf("Input[text] = %q, want concatenated content", uses[0].Input["text"])
	}
}

func TestExtractCodexToolUses_ModelAndEffortStamping(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-05-07T18:44:49.553Z","type":"session_meta","payload":{"id":"sess-1","cwd":"/p","cli_version":"0.128","model_provider":"openai"}}`,
		`{"timestamp":"2026-05-07T18:44:49.557Z","type":"turn_context","payload":{"model":"gpt-5.5","effort":"high","summary":"none"}}`,
		`{"timestamp":"2026-05-07T18:44:50.000Z","type":"event_msg","payload":{"type":"agent_message","message":"hello"}}`,
		`{"timestamp":"2026-05-07T18:45:50.000Z","type":"turn_context","payload":{"model":"gpt-5.6","effort":"low"}}`,
		`{"timestamp":"2026-05-07T18:45:51.000Z","type":"event_msg","payload":{"type":"agent_message","message":"world"}}`,
	}, "\n")

	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}
	if len(uses) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(uses), uses)
	}
	if uses[0].Model != "gpt-5.5" || uses[0].ReasoningEffort != "high" {
		t.Errorf("uses[0] model/effort = %q/%q, want gpt-5.5/high", uses[0].Model, uses[0].ReasoningEffort)
	}
	if uses[1].Model != "gpt-5.6" || uses[1].ReasoningEffort != "low" {
		t.Errorf("uses[1] model/effort = %q/%q, want gpt-5.6/low", uses[1].Model, uses[1].ReasoningEffort)
	}
}

func TestExtractCodexToolUses_MapsCodexControlCalls(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-06-01T10:00:00Z","type":"session_meta","payload":{"id":"sess-1","cwd":"/p"}}`,
		`{"timestamp":"2026-06-01T10:00:01Z","type":"response_item","payload":{"type":"function_call","name":"update_plan","call_id":"call-plan","arguments":"{\"plan\":[{\"step\":\"Inspect Arthas\",\"status\":\"completed\"},{\"step\":\"Run tests\",\"status\":\"in_progress\"}]}"}}`,
		`{"timestamp":"2026-06-01T10:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-plan","output":"ok"}}`,
		`{"timestamp":"2026-06-01T10:00:03Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"call-ask","arguments":"{\"questions\":[{\"id\":\"scope\",\"question\":\"Which Arthas scope?\"}]}"}}`,
	}, "\n")

	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}
	if len(uses) != 2 {
		t.Fatalf("expected 2 tool uses, got %d: %+v", len(uses), uses)
	}
	if uses[0].Tool != "TodoWrite" {
		t.Fatalf("update_plan tool = %q, want TodoWrite", uses[0].Tool)
	}
	todos, ok := uses[0].Input["todos"].([]any)
	if !ok || len(todos) != 2 {
		t.Fatalf("update_plan todos = %#v, want 2 plan items", uses[0].Input["todos"])
	}
	if uses[1].Tool != "AskUserQuestion" {
		t.Fatalf("request_user_input tool = %q, want AskUserQuestion", uses[1].Tool)
	}
}

func TestExtractCodexToolUses_RolloutChatAndEvents(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-07-08T11:19:57.028Z","type":"session_meta","payload":{"id":"sess-rollout","cwd":"/repo","cli_version":"0.143.0","model_provider":"openai"}}`,
		`{"timestamp":"2026-07-08T11:19:57.028Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1","started_at":"2026-07-08T11:19:57.028Z","model_context_window":258400}}`,
		`{"timestamp":"2026-07-08T11:19:58.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/repo</cwd>\n</environment_context>"}]}}`,
		`{"timestamp":"2026-07-08T11:19:58.758Z","type":"turn_context","payload":{"model":"gpt-5.5","effort":"high"}}`,
		`{"timestamp":"2026-07-08T11:19:58.760Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
		`{"timestamp":"2026-07-08T11:19:58.760Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
		`{"timestamp":"2026-07-08T11:20:00.403Z","type":"event_msg","payload":{"type":"agent_message","message":"Hi. What do you want to work on in ` + "`captain`" + `?"}}`,
		`{"timestamp":"2026-07-08T11:20:00.403Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi. What do you want to work on in ` + "`captain`" + `?"}]}}`,
		`{"timestamp":"2026-07-08T11:20:00.432Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"cached_input_tokens":5,"output_tokens":2,"total_tokens":12},"model_context_window":258400}}}`,
		`{"timestamp":"2026-07-08T11:20:00.435Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","duration_ms":3519,"time_to_first_token_ms":3123}}`,
	}, "\n")

	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}

	var got []string
	var texts []string
	for _, use := range uses {
		got = append(got, use.Tool)
		if text, _ := use.Input["text"].(string); text != "" {
			texts = append(texts, text)
		}
	}
	want := []string{"TaskStarted", "User", "Assistant", "TokenCount", "TaskComplete"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v; uses=%+v", got, want, uses)
	}
	if len(texts) != 2 || texts[0] != "hi" || texts[1] != "Hi. What do you want to work on in `captain`?" {
		t.Fatalf("texts = %v", texts)
	}
	for _, use := range uses {
		if use.Model != "" && use.Model != "gpt-5.5" {
			t.Fatalf("unexpected model on %s: %q", use.Tool, use.Model)
		}
	}
}

func TestExtractCodexToolUses_SplitsTaggedAssistantMessageAndDedupesSchemas(t *testing.T) {
	message := `<proposed_plan>
# Parser plan

- Split wrappers
</proposed_plan>

Source used: https://example.com/change

<oai-mem-citation>
<citation_entries>
MEMORY.md:10-12|note=[parser seam]
</citation_entries>
<rollout_ids>
019f3754-ecfa-7323-a76b-a0205ea30bbe
</rollout_ids>
</oai-mem-citation>`
	quoted := strings.ReplaceAll(strings.ReplaceAll(message, `\`, `\\`), `"`, `\"`)
	quoted = strings.ReplaceAll(quoted, "\n", `\n`)
	stream := strings.Join([]string{
		`{"timestamp":"2026-07-10T10:00:00Z","type":"session_meta","payload":{"id":"sess-plan","cwd":"/repo"}}`,
		`{"timestamp":"2026-07-10T10:00:01Z","type":"event_msg","payload":{"type":"agent_message","message":"` + quoted + `"}}`,
		`{"timestamp":"2026-07-10T10:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + quoted + `"}]}}`,
	}, "\n")

	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}
	if len(uses) != 3 {
		t.Fatalf("uses = %+v, want Plan, Assistant, MemoryCitation", uses)
	}
	if uses[0].Tool != "Plan" || uses[0].Input["content"] != "# Parser plan\n\n- Split wrappers" {
		t.Fatalf("plan = %+v", uses[0])
	}
	if uses[1].Tool != "Assistant" || uses[1].Input["text"] != "Source used: https://example.com/change" {
		t.Fatalf("assistant = %+v", uses[1])
	}
	if uses[2].Tool != "MemoryCitation" || uses[2].Input["event"] != "memory_citation" {
		t.Fatalf("citation = %+v", uses[2])
	}
	if got, ok := uses[2].Input["rollout_ids"].([]string); !ok || len(got) != 1 || got[0] != "019f3754-ecfa-7323-a76b-a0205ea30bbe" {
		t.Fatalf("rollout ids = %#v", uses[2].Input["rollout_ids"])
	}
}

func TestExtractCodexToolUses_ClassifiesAgentsInstructionsAsSystem(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-07-10T09:49:37.000Z","type":"session_meta","payload":{"id":"sess-system","cwd":"/repo"}}`,
		`{"timestamp":"2026-07-10T09:49:37.100Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"  # AGENTS.md instructions for /repo\n\nAlways test."}]}}`,
		`{"timestamp":"2026-07-10T09:49:37.100Z","type":"event_msg","payload":{"type":"user_message","message":"# AGENTS.md instructions for /repo\n\nAlways test."}}`,
		`{"timestamp":"2026-07-10T09:49:37.200Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Fix the parser"}]}}`,
		`{"timestamp":"2026-07-10T09:49:37.200Z","type":"event_msg","payload":{"type":"user_message","message":"Fix the parser"}}`,
	}, "\n")

	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}
	if len(uses) != 2 {
		t.Fatalf("uses = %+v, want one System and one User", uses)
	}
	if uses[0].Tool != "System" || uses[1].Tool != "User" {
		t.Fatalf("tools = %q, %q, want System, User", uses[0].Tool, uses[1].Tool)
	}
}

func TestCodexUserMessageTool_ClassifiesRecommendedPluginsAgentsEnvelopeAsSystem(t *testing.T) {
	text := "<recommended_plugins>system recommendations</recommended_plugins>" +
		"# AGENTS.md instructions for /repo\n<INSTRUCTIONS>Always test.</INSTRUCTIONS>"
	tool, ok := codexUserMessageTool(text)
	if !ok || tool != "System" {
		t.Fatalf("codexUserMessageTool = %q, %v, want System, true", tool, ok)
	}
}

func TestReadCodexSessionInfo_ModelAndEffort(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-05-07T18:44:49.553Z","type":"session_meta","payload":{"id":"sess-1","cwd":"/p","cli_version":"0.128","model_provider":"openai","originator":"codex_exec","git":{"branch":"main","commit_hash":"abc"}}}`,
		`{"timestamp":"2026-05-07T18:44:49.557Z","type":"turn_context","payload":{"model":"gpt-5.5","effort":"high","summary":"none"}}`,
	}, "\n")
	dir := t.TempDir()
	path := dir + "/sess.jsonl"
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := ReadCodexSessionInfo(path)
	if err != nil {
		t.Fatalf("ReadCodexSessionInfo: %v", err)
	}
	if info == nil {
		t.Fatal("info should not be nil")
	}
	if info.Model != "gpt-5.5" || info.ReasoningEffort != "high" {
		t.Errorf("model/effort = %q/%q, want gpt-5.5/high", info.Model, info.ReasoningEffort)
	}
	if info.GitBranch != "main" || info.ID != "sess-1" || info.ModelProvider != "openai" {
		t.Errorf("info = %+v", info)
	}
}

func TestReadCodexSessionInfo_LiveThreadStarted(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-05-07T18:44:49.553Z","type":"thread.started","thread_id":"019e0365-dc2a-7ad0-a5a8-78936481a928"}`,
		`{"timestamp":"2026-05-07T18:44:49.557Z","type":"turn_context","payload":{"model":"gpt-5-codex","effort":"high"}}`,
	}, "\n")
	dir := t.TempDir()
	path := dir + "/sess.jsonl"
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := ReadCodexSessionInfo(path)
	if err != nil {
		t.Fatalf("ReadCodexSessionInfo: %v", err)
	}
	if info == nil {
		t.Fatal("info should not be nil")
	}
	if info.ID != "019e0365-dc2a-7ad0-a5a8-78936481a928" {
		t.Errorf("ID = %q", info.ID)
	}
	if info.Model != "gpt-5-codex" || info.ReasoningEffort != "high" {
		t.Errorf("model/effort = %q/%q, want gpt-5-codex/high", info.Model, info.ReasoningEffort)
	}
}

func TestReadCodexSessionMetaStopsAtHeader(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-05-07T18:44:49.553Z","type":"session_meta","payload":{"id":"sess-1","cwd":"/p","cli_version":"0.128","model_provider":"openai"}}`,
		`{"timestamp":"2026-05-07T18:44:49.557Z","type":"turn_context","payload":{"model":"gpt-5.5","effort":"high"}}`,
	}, "\n")
	dir := t.TempDir()
	path := dir + "/sess.jsonl"
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := ReadCodexSessionMeta(path)
	if err != nil {
		t.Fatalf("ReadCodexSessionMeta: %v", err)
	}
	if info == nil {
		t.Fatal("info should not be nil")
	}
	if info.ID != "sess-1" || info.CWD != "/p" || info.ModelProvider != "openai" {
		t.Errorf("info = %+v", info)
	}
	if info.Model != "" || info.ReasoningEffort != "" {
		t.Errorf("meta reader should not scan turn_context, got model/effort %q/%q", info.Model, info.ReasoningEffort)
	}
}

func TestUnwrapCodexErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "boom", "boom"},
		{"empty", "", ""},
		{
			"nested error.message",
			`{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"bad model"}}`,
			"bad model",
		},
		{
			"top-level message",
			`{"message":"top"}`,
			"top",
		},
		{
			"non-json passthrough",
			"not { json",
			"not { json",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := unwrapCodexErrorMessage(tc.in)
			if got != tc.want {
				t.Errorf("unwrapCodexErrorMessage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
