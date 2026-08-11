package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
)

func TestBuildClaudeCLIArgs(t *testing.T) {
	req := ai.Request{
		Prompt: api.Prompt{
			User:         "hello",
			System:       "system prompt",
			AppendSystem: "append system",
			SchemaJSON:   json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string","minLength":2}}}`),
		},
		Model:     api.Model{Effort: api.EffortHigh},
		Budget:    api.Budget{Cost: 1.25},
		SessionID: "sess-1",
		Memory: api.Memory{
			Skills:     []string{"/skills/a", " "},
			SkipSkills: true,
			Bare:       true,
		},
		Permissions: api.Permissions{
			Mode: api.PermissionAcceptEdits,
			Tools: api.Tools{
				Allow: []string{"Read", "Grep"},
				Deny:  []string{"Bash"},
			},
			MCP: api.MCP{Disabled: true},
		},
	}

	args, cleanup, err := buildClaudeCLIArgs("claude-sonnet-5", req)
	if err != nil {
		t.Fatalf("buildClaudeCLIArgs: %v", err)
	}
	defer cleanup()
	wantPrefix := []string{"-p", "--verbose", "--output-format", "stream-json"}
	if !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("args prefix = %v, want %v", args[:len(wantPrefix)], wantPrefix)
	}
	requireFlagValue(t, args, "--model", "claude-sonnet-5")
	requireFlagValue(t, args, "--system-prompt", "system prompt")
	requireFlagValue(t, args, "--append-system-prompt", "append system")
	requireFlagValue(t, args, "--resume", "sess-1")
	requireFlagValue(t, args, "--effort", "high")
	requireFlagValue(t, args, "--max-budget-usd", "1.25")
	requireFlagValue(t, args, "--permission-mode", "acceptEdits")
	// The lists are the canonical policy map projected back out, so they are
	// sorted rather than in caller order — order is not meaningful to claude, and
	// a stable order keeps the command line reproducible.
	requireFlagValue(t, args, "--allowedTools", "Grep,Read")
	requireFlagValue(t, args, "--disallowedTools", "Bash")
	requireFlagValue(t, args, "--plugin-dir", "/skills/a")
	requireFlagValue(t, args, "--mcp-config", `{"mcpServers":{}}`)
	requireHasArg(t, args, "--strict-mcp-config")
	requireHasArg(t, args, "--disable-slash-commands")
	requireHasArg(t, args, "--bare")
	schema := flagValue(t, args, "--json-schema")
	if !json.Valid([]byte(schema)) {
		t.Fatalf("--json-schema = %q, want valid JSON", schema)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	answer := decoded["properties"].(map[string]any)["answer"].(map[string]any)
	if _, ok := answer["minLength"]; ok {
		t.Fatalf("--json-schema should strip minLength key, got %s", schema)
	}
	if answer["description"] == "" {
		t.Fatalf("--json-schema should describe removed constraints, got %s", schema)
	}
}

func TestClaudeEntryEventsMapsTextThinkingAndResult(t *testing.T) {
	entry := claude.HistoryEntry{
		SessionID: "sess-1",
		Message: claude.Message{
			Model: "sonnet",
			Role:  claude.MessageRoleAssistant,
			Usage: &claude.Usage{InputTokens: 11, OutputTokens: 7, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
			Content: []claude.ContentBlock{
				{Type: claude.ContentTypeThinking, Thinking: "thinking"},
				{Type: claude.ContentTypeText, Text: "hello"},
				{
					Type:  claude.ContentTypeToolUse,
					ID:    "result-1",
					Name:  "Result",
					Input: json.RawMessage(`{"result":"{\"answer\":\"42\"}","total_cost_usd":0.5}`),
				},
			},
		},
	}

	events := claudeEntryEvents(entry, "fallback")
	if len(events) != 3 {
		t.Fatalf("events = %+v, want 3", events)
	}
	if events[0].Kind != ai.EventThinking || events[0].Text != "thinking" {
		t.Fatalf("thinking event = %+v", events[0])
	}
	if events[1].Kind != ai.EventText || events[1].Text != "hello" {
		t.Fatalf("text event = %+v", events[1])
	}
	result := events[2]
	if result.Kind != ai.EventResult || !result.Success || result.Text != `{"answer":"42"}` {
		t.Fatalf("result event = %+v", result)
	}
	if string(result.StructuredData) != `{"answer":"42"}` {
		t.Fatalf("StructuredData = %s, want answer JSON", result.StructuredData)
	}
	if result.Usage == nil || result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

// resultLine is a verbatim `type: result` line from claude 2.1.220. The turn's
// token counts live at the top level of this line, not inside a message — the
// synthetic entry the reader builds for it therefore has no Message.Usage, and
// reading only that field silently drops every claude-cli run's usage.
const resultLine = `{"is_error":false,"duration_api_ms":5,"num_turns":1,"stop_reason":"end_turn",` +
	`"session_id":"a32c3053-b70a-4f4f-9e0e-9d1d4fb48e8e","total_cost_usd":0.000162,` +
	`"usage":{"input_tokens":14,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":8},` +
	`"terminal_reason":"completed","subtype":"success","result":"The capital of France is Paris.","type":"result"}`

func TestClaudeEntryEventsReadsUsageFromAStreamJSONResultLine(t *testing.T) {
	iterator := claude.NewStreamJSONIterator(strings.NewReader(resultLine))
	if !iterator.Next() {
		t.Fatalf("the result line must yield an entry: %v", iterator.Err())
	}

	events := claudeEntryEvents(iterator.Entry(), "sonnet")
	if len(events) != 1 || events[0].Kind != ai.EventResult {
		t.Fatalf("events = %+v, want one result event", events)
	}
	result := events[0]
	if result.SessionID != "a32c3053-b70a-4f4f-9e0e-9d1d4fb48e8e" {
		t.Fatalf("session id = %q", result.SessionID)
	}
	want := ai.Usage{InputTokens: 14, OutputTokens: 8, CacheReadTokens: 3, CacheWriteTokens: 2}
	if result.Usage == nil || *result.Usage != want {
		t.Fatalf("usage = %+v, want %+v", result.Usage, want)
	}
}

func requireHasArg(t *testing.T, args []string, want string) {
	t.Helper()
	for _, arg := range args {
		if arg == want {
			return
		}
	}
	t.Fatalf("args %v do not contain %q", args, want)
}

func requireFlagValue(t *testing.T, args []string, flag, want string) {
	t.Helper()
	got := flagValue(t, args, flag)
	if got != want {
		t.Fatalf("%s = %q, want %q (args: %v)", flag, got, want, args)
	}
}

// requireFlagPair asserts a (flag, value) pair appears anywhere in args. Use it
// for repeatable flags — codex's -c carries several unrelated overrides, so
// requireFlagValue's first-match-wins lookup answers about whichever one the
// builder happened to emit first.
func requireFlagPair(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i, arg := range args {
		if arg == flag && i+1 < len(args) && args[i+1] == want {
			return
		}
	}
	t.Fatalf("args %v do not contain %s %q", args, flag, want)
}

func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, arg := range args {
		if arg == flag {
			if i+1 >= len(args) {
				t.Fatalf("%s has no value in args %v", flag, args)
			}
			return args[i+1]
		}
	}
	t.Fatalf("args %v do not contain flag %s", args, flag)
	return ""
}
