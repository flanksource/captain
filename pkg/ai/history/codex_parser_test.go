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
