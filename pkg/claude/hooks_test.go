package claude

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestHooksConfig_Unmarshal(t *testing.T) {
	input := `{
		"hooks": {
			"PreToolUse": [
				{
					"matcher": "Bash",
					"hooks": [
						{"type": "command", "command": "echo test", "timeout": 60}
					]
				}
			]
		}
	}`

	var config HooksConfig
	if err := json.Unmarshal([]byte(input), &config); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	matchers, ok := config.Hooks[HookEventPreToolUse]
	if !ok || len(matchers) != 1 {
		t.Fatalf("expected 1 PreToolUse matcher, got %d", len(matchers))
	}

	if matchers[0].Matcher != "Bash" {
		t.Errorf("expected matcher 'Bash', got %q", matchers[0].Matcher)
	}

	if len(matchers[0].Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(matchers[0].Hooks))
	}

	hook := matchers[0].Hooks[0]
	if hook.Type != HookTypeCommand || hook.Command != "echo test" || hook.Timeout != 60 {
		t.Errorf("unexpected hook: %+v", hook)
	}
}

func TestHookInput_Unmarshal(t *testing.T) {
	input := `{
		"session_id": "abc123",
		"tool_name": "Bash",
		"tool_input": {"command": "ls -la"}
	}`

	var hookInput HookInput
	if err := json.Unmarshal([]byte(input), &hookInput); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if hookInput.SessionID != "abc123" {
		t.Errorf("expected session_id 'abc123', got %q", hookInput.SessionID)
	}

	if hookInput.ToolName != "Bash" {
		t.Errorf("expected tool_name 'Bash', got %q", hookInput.ToolName)
	}

	var bashInput BashToolInput
	if err := json.Unmarshal(hookInput.ToolInput, &bashInput); err != nil {
		t.Fatalf("unmarshal tool_input failed: %v", err)
	}

	if bashInput.Command != "ls -la" {
		t.Errorf("expected command 'ls -la', got %q", bashInput.Command)
	}
}

func TestHookOutput_Marshal(t *testing.T) {
	output := HookOutput{
		Continue:   false,
		StopReason: "blocked by policy",
		HookSpecificOutput: &HookSpecificOutput{
			PermissionDecision:       PermissionDeny,
			PermissionDecisionReason: "dangerous command",
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed["continue"] != false {
		t.Errorf("expected continue=false")
	}

	if parsed["stopReason"] != "blocked by policy" {
		t.Errorf("unexpected stopReason: %v", parsed["stopReason"])
	}

	specific := parsed["hookSpecificOutput"].(map[string]any)
	if specific["permissionDecision"] != "deny" {
		t.Errorf("unexpected permissionDecision: %v", specific["permissionDecision"])
	}
}

func TestHookInput_Unmarshal_LifecycleEvents(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    HookInput
	}{
		{
			name: "SessionStart",
			payload: `{
				"session_id": "abc123",
				"transcript_path": "/Users/x/.claude/projects/-repo/abc123.jsonl",
				"cwd": "/repo",
				"hook_event_name": "SessionStart",
				"source": "startup"
			}`,
			want: HookInput{
				SessionID:      "abc123",
				TranscriptPath: "/Users/x/.claude/projects/-repo/abc123.jsonl",
				CWD:            "/repo",
				HookEventName:  "SessionStart",
				Source:         "startup",
			},
		},
		{
			name: "SessionEnd",
			payload: `{
				"session_id": "abc123",
				"transcript_path": "/Users/x/.claude/projects/-repo/abc123.jsonl",
				"cwd": "/repo",
				"hook_event_name": "SessionEnd",
				"reason": "prompt_input_exit"
			}`,
			want: HookInput{
				SessionID:      "abc123",
				TranscriptPath: "/Users/x/.claude/projects/-repo/abc123.jsonl",
				CWD:            "/repo",
				HookEventName:  "SessionEnd",
				Reason:         "prompt_input_exit",
			},
		},
		{
			name: "Stop",
			payload: `{
				"session_id": "abc123",
				"transcript_path": "/Users/x/.claude/projects/-repo/abc123.jsonl",
				"cwd": "/repo",
				"hook_event_name": "Stop",
				"last_assistant_message": "done"
			}`,
			want: HookInput{
				SessionID:            "abc123",
				TranscriptPath:       "/Users/x/.claude/projects/-repo/abc123.jsonl",
				CWD:                  "/repo",
				HookEventName:        "Stop",
				LastAssistantMessage: "done",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got HookInput
			if err := json.Unmarshal([]byte(tc.payload), &got); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestHooksConfig_Marshal_SessionLifecycle(t *testing.T) {
	config := HooksConfig{Hooks: map[HookEventType][]HookMatcher{
		HookEventSessionStart: {{Hooks: []Hook{{Type: HookTypeCommand, Command: "captain hook monitor notify --provider claude", Timeout: 10}}}},
		HookEventSessionEnd:   {{Hooks: []Hook{{Type: HookTypeCommand, Command: "captain hook monitor notify --provider claude", Timeout: 10}}}},
	}}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed map[string]map[string][]map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	for _, event := range []string{"SessionStart", "SessionEnd"} {
		matchers := parsed["hooks"][event]
		if len(matchers) != 1 {
			t.Fatalf("%s: expected 1 matcher, got %d", event, len(matchers))
		}
		if _, exists := matchers[0]["matcher"]; exists {
			t.Errorf("%s: matcher key should be omitted for lifecycle events", event)
		}
		hooks := matchers[0]["hooks"].([]any)
		hook := hooks[0].(map[string]any)
		if hook["type"] != "command" || hook["timeout"] != float64(10) {
			t.Errorf("%s: unexpected hook entry: %+v", event, hook)
		}
	}
}

func TestHookOutput_Marshal_OmitsNil(t *testing.T) {
	output := HookOutput{Continue: true}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if _, exists := parsed["hookSpecificOutput"]; exists {
		t.Error("hookSpecificOutput should be omitted when nil")
	}
}
