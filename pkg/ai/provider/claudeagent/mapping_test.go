package claudeagent

import (
	"encoding/json"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testModel = "claude-sonnet-5"

func mustMap(t *testing.T, method, params string) ai.Event {
	t.Helper()
	ev, ok := mapNotification(method, json.RawMessage(params), testModel)
	require.True(t, ok, "expected %s to map to an event", method)
	return ev
}

func TestMapNotification_SessionInit(t *testing.T) {
	ev := mustMap(t, notifySessionInit,
		`{"session_id":"sess-123","model":"claude-sonnet-4-5","tools":["Read","Bash"]}`)

	assert.Equal(t, ai.EventSystem, ev.Kind)
	assert.Equal(t, "SessionInit", ev.Tool)
	assert.Equal(t, "sess-123", ev.SessionID)
	// The notification's own model wins over the fallback.
	assert.Equal(t, "claude-sonnet-4-5", ev.Model)

	tu, ok := ev.Raw.(claude.ToolUse)
	require.True(t, ok, "Raw should be a claude.ToolUse")
	assert.Equal(t, "SessionInit", tu.Tool)
	assert.Equal(t, claudeSource, tu.Source)
	assert.Equal(t, "sess-123", tu.SessionID)
	assert.Equal(t, "claude-sonnet-4-5", tu.Model)
}

func TestMapNotification_SessionInitFallbackModel(t *testing.T) {
	ev := mustMap(t, notifySessionInit, `{"session_id":"s1"}`)
	assert.Equal(t, testModel, ev.Model, "empty model falls back to the provider model")
	tu := ev.Raw.(claude.ToolUse)
	assert.Equal(t, testModel, tu.Model)
}

func TestMapNotification_Text(t *testing.T) {
	ev := mustMap(t, notifyMessageText, `{"text":"hello world"}`)
	assert.Equal(t, ai.EventText, ev.Kind)
	assert.Equal(t, "hello world", ev.Text)
	assert.Equal(t, testModel, ev.Model)
	assert.Nil(t, ev.Raw, "text rows carry no claude.ToolUse")
}

func TestMapNotification_EmptyTextDropped(t *testing.T) {
	_, ok := mapNotification(notifyMessageText, json.RawMessage(`{"text":""}`), testModel)
	assert.False(t, ok, "empty text should not produce an event")
}

func TestMapNotification_Thinking(t *testing.T) {
	ev := mustMap(t, notifyMessageThink, `{"text":"let me think"}`)
	assert.Equal(t, ai.EventThinking, ev.Kind)
	assert.Equal(t, "let me think", ev.Text)
	assert.Equal(t, testModel, ev.Model)
}

func TestMapNotification_EmptyThinkingDropped(t *testing.T) {
	_, ok := mapNotification(notifyMessageThink, json.RawMessage(`{"text":""}`), testModel)
	assert.False(t, ok)
}

func TestMapNotification_ToolUse(t *testing.T) {
	ev := mustMap(t, notifyToolUse,
		`{"tool":"Bash","id":"toolu_42","input":{"command":"ls -la"}}`)

	assert.Equal(t, ai.EventToolUse, ev.Kind)
	assert.Equal(t, "Bash", ev.Tool)
	assert.Equal(t, "toolu_42", ev.ToolCallID)
	assert.Equal(t, "ls -la", ev.Input["command"])

	tu, ok := ev.Raw.(claude.ToolUse)
	require.True(t, ok, "Raw should be a claude.ToolUse")
	assert.Equal(t, "Bash", tu.Tool)
	assert.Equal(t, "toolu_42", tu.ToolUseID)
	assert.Equal(t, claudeSource, tu.Source)
	assert.Equal(t, testModel, tu.Model)
	assert.Equal(t, "ls -la", tu.Input["command"])
}

func TestMapNotification_ToolResult(t *testing.T) {
	ev := mustMap(t, notifyToolResult,
		`{"id":"toolu_42","content":"total 8\ndrwxr-xr-x","is_error":false}`)

	assert.Equal(t, ai.EventToolResult, ev.Kind)
	assert.Equal(t, "toolu_42", ev.ToolCallID, "result correlates to its call by id")
	assert.Equal(t, "total 8\ndrwxr-xr-x", ev.Text)
	assert.True(t, ev.Success)

	tu, ok := ev.Raw.(claude.ToolUse)
	require.True(t, ok, "Raw should be a claude.ToolUse")
	assert.Equal(t, "toolu_42", tu.ToolUseID)
	assert.Equal(t, "total 8\ndrwxr-xr-x", tu.Response)
	assert.False(t, tu.IsError)
	assert.Equal(t, claudeSource, tu.Source)
}

func TestMapNotification_ToolResultError(t *testing.T) {
	ev := mustMap(t, notifyToolResult,
		`{"id":"toolu_7","content":"permission denied","is_error":true}`)

	assert.Equal(t, ai.EventToolResult, ev.Kind)
	assert.False(t, ev.Success, "is_error true => Success false")
	tu := ev.Raw.(claude.ToolUse)
	assert.True(t, tu.IsError)
}

func TestMapNotification_TurnCompletedSuccess(t *testing.T) {
	ev := mustMap(t, notifyTurnDone, `{
		"success": true,
		"session_id": "sess-9",
		"cost_usd": 0.0123,
		"num_turns": 3,
		"result_text": "done",
		"usage": {"input_tokens": 100, "output_tokens": 42, "cache_read_input_tokens": 7}
	}`)

	assert.Equal(t, ai.EventResult, ev.Kind)
	assert.Equal(t, "Result", ev.Tool)
	assert.True(t, ev.Success)
	assert.Equal(t, "sess-9", ev.SessionID)
	assert.InDelta(t, 0.0123, ev.CostUSD, 1e-9)

	require.NotNil(t, ev.Usage)
	assert.Equal(t, 100, ev.Usage.InputTokens)
	assert.Equal(t, 42, ev.Usage.OutputTokens)
	assert.Equal(t, 7, ev.Usage.CacheReadTokens)

	// Input carries the renderer-facing fields.
	assert.Equal(t, false, ev.Input["is_error"])
	assert.Equal(t, "done", ev.Input["result"])
	assert.Equal(t, "sess-9", ev.Input["session_id"])

	tu, ok := ev.Raw.(claude.ToolUse)
	require.True(t, ok, "Raw should be a claude.ToolUse")
	assert.Equal(t, "Result", tu.Tool)
	assert.Equal(t, claudeSource, tu.Source)
	assert.Equal(t, "sess-9", tu.SessionID)
	assert.Equal(t, 100, tu.InputTokens)
	assert.Equal(t, 42, tu.OutputTokens)
	assert.False(t, tu.IsError)
}

func TestMapNotification_TurnCompletedStructured(t *testing.T) {
	ev := mustMap(t, notifyTurnDone, `{
		"success": true,
		"session_id": "s3",
		"subtype": "success",
		"structured_output": {"company_name": "Anthropic", "founded_year": 2021}
	}`)

	assert.Equal(t, ai.EventResult, ev.Kind)
	assert.Equal(t, "success", ev.Input["subtype"])
	require.NotEmpty(t, ev.StructuredData, "structured_output should ride on the result event")

	var out map[string]any
	require.NoError(t, json.Unmarshal(ev.StructuredData, &out))
	assert.Equal(t, "Anthropic", out["company_name"])
}

func TestMapNotification_TurnCompletedNoStructured(t *testing.T) {
	// A text-mode result and an explicit null both leave StructuredData nil.
	ev := mustMap(t, notifyTurnDone, `{"success": true, "session_id": "s4"}`)
	assert.Nil(t, ev.StructuredData)

	ev = mustMap(t, notifyTurnDone, `{"success": true, "structured_output": null}`)
	assert.Nil(t, ev.StructuredData, "explicit null is not structured output")
}

func TestMapNotification_TurnCompletedFailure(t *testing.T) {
	ev := mustMap(t, notifyTurnDone, `{"success": false, "session_id": "s2"}`)

	assert.Equal(t, ai.EventResult, ev.Kind)
	assert.False(t, ev.Success)
	assert.Equal(t, true, ev.Input["is_error"])

	tu := ev.Raw.(claude.ToolUse)
	assert.True(t, tu.IsError, "failed turn marks the Result row as error")
}

func TestMapNotification_TurnError(t *testing.T) {
	ev := mustMap(t, notifyTurnError, `{"message":"sdk exploded"}`)
	assert.Equal(t, ai.EventError, ev.Kind)
	assert.Equal(t, "sdk exploded", ev.Error)
	assert.Equal(t, testModel, ev.Model)
}

func TestMapNotification_UnknownMethod(t *testing.T) {
	_, ok := mapNotification("totally/unknown", json.RawMessage(`{}`), testModel)
	assert.False(t, ok)
}

func TestDecodeUsage(t *testing.T) {
	t.Run("nil when empty", func(t *testing.T) {
		assert.Nil(t, decodeUsage(nil))
		assert.Nil(t, decodeUsage(json.RawMessage(`{}`)))
	})
	t.Run("populated", func(t *testing.T) {
		u := decodeUsage(json.RawMessage(`{"input_tokens":5,"output_tokens":9,"cache_creation_input_tokens":3}`))
		require.NotNil(t, u)
		assert.Equal(t, 5, u.InputTokens)
		assert.Equal(t, 9, u.OutputTokens)
		assert.Equal(t, 3, u.CacheWriteTokens)
	})
}

func TestAliasModel(t *testing.T) {
	cases := map[string]string{
		"claude-agent-sonnet":   "claude-sonnet-5",
		"claude-agent-opus":     "claude-opus-4-8",
		"claude-code-haiku":     "claude-haiku-4-5",
		"claude-sonnet-4-5":     "claude-sonnet-4-6",
		"claude-agent-":         "claude-sonnet-5",
		"":                      "claude-sonnet-5",
		"claude-agent-opus-4-1": "claude-opus-4-8",
	}
	for in, want := range cases {
		assert.Equalf(t, want, aliasModel(in), "aliasModel(%q)", in)
	}
}

func TestNestingEnvOverrides(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=cli",
		"HOME=/home/x",
		"CLAUDECODE_EXTRA=foo",
	}
	got := nestingEnvOverrides(env)
	assert.Equal(t, map[string]string{
		"CLAUDECODE":             "",
		"CLAUDE_CODE_ENTRYPOINT": "",
		"CLAUDECODE_EXTRA":       "",
	}, got)
}
