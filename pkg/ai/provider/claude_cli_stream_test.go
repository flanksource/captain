package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/claude"
)

// streamFixture is a realistic stream-json transcript: a system/init line
// (becomes a synthetic SessionInit tool_use), an assistant message containing
// thinking + text + a real tool_use block, then a final result line (becomes
// a synthetic Result tool_use carrying total_cost_usd, usage, is_error).
const streamFixture = `{"type":"system","subtype":"init","session_id":"sess-fixture-1","timestamp":"2026-05-06T12:00:00Z","cwd":"/repo","model":"claude-3-5-sonnet-20241022","tools":["Read","Write"],"permissionMode":"acceptEdits","apiKeySource":"env","claude_code_version":"1.0.0"}
{"type":"assistant","session_id":"sess-fixture-1","timestamp":"2026-05-06T12:00:01Z","uuid":"u-1","message":{"role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"thinking","thinking":"Need to inspect the file."},{"type":"text","text":"I'll read it."},{"type":"tool_use","id":"tu-1","name":"Read","input":{"file_path":"/repo/foo.go"}}]}}
{"type":"result","subtype":"success","session_id":"sess-fixture-1","timestamp":"2026-05-06T12:00:02Z","is_error":false,"num_turns":1,"total_cost_usd":0.0123,"duration_ms":1500,"usage":{"input_tokens":42,"output_tokens":17,"cache_read_input_tokens":3,"cache_creation_input_tokens":1}}
`

// drainHistoryToEvents runs the same path that ExecuteStream's goroutine takes
// (NewStreamJSONIterator → mapHistoryEntry) without spawning a subprocess.
func drainHistoryToEvents(t *testing.T, jsonl, model string) []ai.Event {
	t.Helper()
	it := claude.NewStreamJSONIterator(strings.NewReader(jsonl))
	var events []ai.Event
	for it.Next() {
		events = append(events, mapHistoryEntry(it.Entry(), model)...)
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator err: %v", err)
	}
	return events
}

func TestMapHistoryEntry_StreamFixtureCoversAllEventKinds(t *testing.T) {
	events := drainHistoryToEvents(t, streamFixture, "claude-3-5-sonnet-20241022")

	// Expect: SessionInit (EventSystem) + thinking + text + tool_use + Result (EventResult).
	if len(events) != 5 {
		t.Fatalf("event count = %d, want 5; events=%+v", len(events), events)
	}

	if events[0].Kind != ai.EventSystem {
		t.Errorf("events[0].Kind = %q, want %q", events[0].Kind, ai.EventSystem)
	}
	if events[0].SessionID != "sess-fixture-1" {
		t.Errorf("events[0].SessionID = %q, want sess-fixture-1", events[0].SessionID)
	}

	if events[1].Kind != ai.EventThinking {
		t.Errorf("events[1].Kind = %q, want %q", events[1].Kind, ai.EventThinking)
	}
	if events[1].Text != "Need to inspect the file." {
		t.Errorf("events[1].Text = %q", events[1].Text)
	}

	if events[2].Kind != ai.EventText {
		t.Errorf("events[2].Kind = %q, want %q", events[2].Kind, ai.EventText)
	}
	if events[2].Text != "I'll read it." {
		t.Errorf("events[2].Text = %q", events[2].Text)
	}

	if events[3].Kind != ai.EventToolUse {
		t.Errorf("events[3].Kind = %q, want %q", events[3].Kind, ai.EventToolUse)
	}
	if events[3].Tool != "Read" {
		t.Errorf("events[3].Tool = %q, want Read", events[3].Tool)
	}
	if got, _ := events[3].Input["file_path"].(string); got != "/repo/foo.go" {
		t.Errorf("events[3].Input[file_path] = %q, want /repo/foo.go", got)
	}

	if events[4].Kind != ai.EventResult {
		t.Errorf("events[4].Kind = %q, want %q", events[4].Kind, ai.EventResult)
	}
	if !events[4].Success {
		t.Errorf("events[4].Success = false, want true (is_error=false)")
	}
	if events[4].CostUSD != 0.0123 {
		t.Errorf("events[4].CostUSD = %v, want 0.0123", events[4].CostUSD)
	}
	if events[4].Usage == nil {
		t.Fatal("events[4].Usage is nil")
	}
	if events[4].Usage.InputTokens != 42 || events[4].Usage.OutputTokens != 17 {
		t.Errorf("events[4].Usage = %+v, want input=42 output=17", events[4].Usage)
	}
	if events[4].Usage.CacheReadTokens != 3 || events[4].Usage.CacheWriteTokens != 1 {
		t.Errorf("events[4].Usage cache = read:%d write:%d, want 3/1",
			events[4].Usage.CacheReadTokens, events[4].Usage.CacheWriteTokens)
	}
}

func TestMapHistoryEntry_ResultIsErrorMapsToSuccessFalse(t *testing.T) {
	jsonl := `{"type":"result","subtype":"error","session_id":"s","timestamp":"t","is_error":true,"error":"rate limited","total_cost_usd":0.001,"usage":{"input_tokens":1,"output_tokens":0}}` + "\n"
	events := drainHistoryToEvents(t, jsonl, "claude-3-5-sonnet")
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].Kind != ai.EventResult {
		t.Fatalf("Kind = %q, want %q", events[0].Kind, ai.EventResult)
	}
	if events[0].Success {
		t.Error("Success = true, want false (is_error=true)")
	}
	if events[0].Error != "rate limited" {
		t.Errorf("Error = %q, want rate limited", events[0].Error)
	}
}

func TestMapHistoryEntry_AssistantApiErrorEmitsErrorEvent(t *testing.T) {
	// An assistant line carrying a top-level error field becomes an ApiError
	// synthetic tool_use, which our mapper turns into ai.EventError.
	jsonl := `{"type":"assistant","session_id":"s","timestamp":"t","uuid":"u","error":"invalid_request: max tokens","message":{"role":"assistant","content":[{"type":"text","text":"sorry"}]}}` + "\n"
	events := drainHistoryToEvents(t, jsonl, "claude-3-5-sonnet")
	// Expect: 1 text event from the assistant content + 1 ApiError event.
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2; events=%+v", len(events), events)
	}
	if events[1].Kind != ai.EventError {
		t.Errorf("events[1].Kind = %q, want %q", events[1].Kind, ai.EventError)
	}
	if !strings.Contains(events[1].Error, "invalid_request") {
		t.Errorf("events[1].Error = %q, want to contain invalid_request", events[1].Error)
	}
}

func TestCoalesceStream_FixtureProducesResponse(t *testing.T) {
	events := drainHistoryToEvents(t, streamFixture, "claude-3-5-sonnet-20241022")
	ch := make(chan ai.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)

	resp, err := CoalesceStream(context.Background(), "claude-3-5-sonnet-20241022", ch, time.Now())
	if err != nil {
		t.Fatalf("CoalesceStream err: %v", err)
	}
	if resp.Text != "I'll read it." {
		t.Errorf("Text = %q", resp.Text)
	}
	if resp.Usage.InputTokens != 42 {
		t.Errorf("Usage.InputTokens = %d, want 42", resp.Usage.InputTokens)
	}
}

func TestCoalesceStream_ResultErrorReturnsError(t *testing.T) {
	jsonl := `{"type":"result","subtype":"error","session_id":"s","timestamp":"t","is_error":true,"error":"upstream 500"}` + "\n"
	events := drainHistoryToEvents(t, jsonl, "m")
	ch := make(chan ai.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)

	resp, err := CoalesceStream(context.Background(), "m", ch, time.Now())
	if err == nil {
		t.Fatalf("expected error, got resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), "upstream 500") {
		t.Errorf("err = %v, want to mention upstream 500", err)
	}
}

func TestBuildClaudeStreamArgs_AppliesAllRequestKnobs(t *testing.T) {
	req := ai.Request{
		Prompt:         "fix it",
		SystemPrompt:   "you are careful",
		PermissionMode: "acceptEdits",
		StrictMCP:      true,
		MaxTurns:       5,
		SessionID:      "resume-me",
	}
	args, err := buildClaudeStreamArgs("claude-code-opus", req)
	if err != nil {
		t.Fatalf("buildClaudeStreamArgs: %v", err)
	}

	have := strings.Join(args, " ")
	for _, want := range []string{
		"--output-format stream-json",
		"--verbose",
		"--permission-mode acceptEdits",
		"--strict-mcp-config",
		"--max-turns 5",
		"--resume resume-me",
		"--system-prompt you are careful",
	} {
		if !strings.Contains(have, want) {
			t.Errorf("args missing %q; have %s", want, have)
		}
	}
	if args[len(args)-1] != "fix it" {
		t.Errorf("last arg = %q, want prompt", args[len(args)-1])
	}
}

func TestBuildClaudeStreamArgs_OmitsZeroValueKnobs(t *testing.T) {
	args, err := buildClaudeStreamArgs("claude-code-opus", ai.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("buildClaudeStreamArgs: %v", err)
	}
	have := strings.Join(args, " ")
	for _, forbidden := range []string{
		"--permission-mode",
		"--strict-mcp-config",
		"--max-turns",
		"--resume",
		"--system-prompt",
		"--allowedTools",
		"--disallowedTools",
		"--disable-slash-commands",
		"--plugin-dir",
		"--setting-sources",
		"--bare",
		"--mcp-config",
		"--append-system-prompt",
	} {
		if strings.Contains(have, forbidden) {
			t.Errorf("args contain %q for zero-value request: %s", forbidden, have)
		}
	}
}

func TestBuildClaudeStreamArgs_NewFlags(t *testing.T) {
	tests := []struct {
		name        string
		req         ai.Request
		mustContain []string
		mustNot     []string
		wantErr     bool
	}{
		{
			name: "edit applies safe defaults",
			req:  ai.Request{Prompt: "p", Edit: true},
			mustContain: []string{
				"--permission-mode acceptEdits",
				"--allowedTools Read Edit Write Glob Grep",
			},
		},
		{
			name: "edit honours explicit allowed-tools override",
			req: ai.Request{
				Prompt:       "p",
				Edit:         true,
				AllowedTools: []string{"Read", "Bash"},
			},
			mustContain: []string{
				"--permission-mode acceptEdits",
				"--allowedTools Read Bash",
			},
			mustNot: []string{"Read Edit Write Glob Grep"},
		},
		{
			name: "allowlist demotes bypassPermissions",
			req: ai.Request{
				Prompt:         "p",
				PermissionMode: "bypassPermissions",
				AllowedTools:   []string{"Read"},
			},
			mustContain: []string{"--permission-mode default", "--allowedTools Read"},
			mustNot:     []string{"--permission-mode bypassPermissions"},
		},
		{
			name: "no-mcp emits empty inline config",
			req:  ai.Request{Prompt: "p", NoMCP: true},
			mustContain: []string{
				`--strict-mcp-config --mcp-config {"mcpServers":{}}`,
			},
		},
		{
			name: "skill controls",
			req: ai.Request{
				Prompt:    "p",
				NoSkills:  true,
				SkillDirs: []string{"/a", "/b"},
			},
			mustContain: []string{
				"--disable-slash-commands",
				"--plugin-dir /a",
				"--plugin-dir /b",
			},
		},
		{
			name:        "no-user drops user from setting-sources",
			req:         ai.Request{Prompt: "p", NoUser: true},
			mustContain: []string{"--setting-sources project,local"},
			mustNot:     []string{"--setting-sources user"},
		},
		{
			name:        "no-project drops project,local",
			req:         ai.Request{Prompt: "p", NoProject: true},
			mustContain: []string{"--setting-sources user"},
			mustNot:     []string{"project,local"},
		},
		{
			name:    "bare flag",
			req:     ai.Request{Prompt: "p", Bare: true, NoMemory: true},
			mustContain: []string{"--bare"},
		},
		{
			name:        "append-system-prompt",
			req:         ai.Request{Prompt: "p", AppendSystemPrompt: "extra rules"},
			mustContain: []string{"--append-system-prompt extra rules"},
		},
		{
			name:    "no-memory without bare errors",
			req:     ai.Request{Prompt: "p", NoMemory: true},
			wantErr: true,
		},
		{
			name:    "no-hooks without bare/no-user/no-project errors",
			req:     ai.Request{Prompt: "p", NoHooks: true},
			wantErr: true,
		},
		{
			name:        "no-hooks with no-user is allowed",
			req:         ai.Request{Prompt: "p", NoHooks: true, NoUser: true},
			mustContain: []string{"--setting-sources project,local"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args, err := buildClaudeStreamArgs("claude-code-opus", tc.req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got args=%v", args)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			have := strings.Join(args, " ")
			for _, want := range tc.mustContain {
				if !strings.Contains(have, want) {
					t.Errorf("args missing %q\n got: %s", want, have)
				}
			}
			for _, forbidden := range tc.mustNot {
				if strings.Contains(have, forbidden) {
					t.Errorf("args contain forbidden %q\n got: %s", forbidden, have)
				}
			}
		})
	}
}

func TestDemoteOnAllowlist(t *testing.T) {
	cases := []struct {
		mode     string
		hasAllow bool
		want     string
	}{
		{"", false, ""},
		{"", true, "default"},
		{"bypassPermissions", true, "default"},
		{"bypassPermissions", false, "bypassPermissions"},
		{"acceptEdits", true, "acceptEdits"},
		{"plan", true, "plan"},
	}
	for _, tc := range cases {
		got := DemoteOnAllowlist(tc.mode, tc.hasAllow)
		if got != tc.want {
			t.Errorf("DemoteOnAllowlist(%q, %v) = %q, want %q", tc.mode, tc.hasAllow, got, tc.want)
		}
	}
}
