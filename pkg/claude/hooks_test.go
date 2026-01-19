package claude

import (
	"encoding/json"
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
			PermissionDecision: PermissionDeny,
			Reason:             "dangerous command",
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
