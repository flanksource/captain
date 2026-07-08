package provider

import (
	"encoding/json"
	"reflect"
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
			SchemaJSON:   json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}}}`),
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

	args, err := buildClaudeCLIArgs("claude-sonnet-5", req)
	if err != nil {
		t.Fatalf("buildClaudeCLIArgs: %v", err)
	}
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
	requireFlagValue(t, args, "--allowedTools", "Read,Grep")
	requireFlagValue(t, args, "--disallowedTools", "Bash")
	requireFlagValue(t, args, "--plugin-dir", "/skills/a")
	requireFlagValue(t, args, "--mcp-config", "{}")
	requireHasArg(t, args, "--strict-mcp-config")
	requireHasArg(t, args, "--disable-slash-commands")
	requireHasArg(t, args, "--bare")
	schema := flagValue(t, args, "--json-schema")
	if !json.Valid([]byte(schema)) {
		t.Fatalf("--json-schema = %q, want valid JSON", schema)
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
