package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFromReader_ClaudeJSONL(t *testing.T) {
	data := []byte(`{"sessionId":"abc-123","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"ls -la"}}]},"uuid":"1","timestamp":"2024-01-01T10:00:00Z"}
{"sessionId":"abc-123","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-2","name":"Read","input":{"file_path":"/tmp/foo.go"}}]},"uuid":"2","timestamp":"2024-01-01T10:01:00Z"}
`)

	result, err := parseFromReader(data)
	require.NoError(t, err)
	assert.Equal(t, claude.FormatClaudeJSONL, result.Format)
	assert.Nil(t, result.CLIOut)
	require.Len(t, result.ToolUses, 2)
	assert.Equal(t, "Bash", result.ToolUses[0].Tool)
	assert.Equal(t, "ls -la", result.ToolUses[0].Input["command"])
	assert.Equal(t, "Read", result.ToolUses[1].Tool)
	assert.Equal(t, "/tmp/foo.go", result.ToolUses[1].Input["file_path"])
}

func TestParseFromReader_CodexJSONL(t *testing.T) {
	data := []byte(`{"timestamp":"2024-01-01T10:00:00Z","type":"session_meta","payload":{"id":"sess-1","cwd":"/tmp/project","type":"session_meta"}}
{"timestamp":"2024-01-01T10:00:01Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":"{\"cmd\":\"echo hello\"}"}}
{"timestamp":"2024-01-01T10:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"Output:\nhello"}}
`)

	result, err := parseFromReader(data)
	require.NoError(t, err)
	assert.Equal(t, claude.FormatCodexJSONL, result.Format)
	assert.Nil(t, result.CLIOut)
	require.NotEmpty(t, result.ToolUses)
	assert.Equal(t, "Bash", result.ToolUses[0].Tool)
	assert.Equal(t, "echo hello", result.ToolUses[0].Input["command"])
	assert.Equal(t, "hello", result.ToolUses[0].Response)
	assert.Equal(t, "codex", result.ToolUses[0].Source)
}

func TestParseFromReader_ClaudeStreamJSON(t *testing.T) {
	data := []byte(`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"sess-1","model":"claude-sonnet-4-20250514","tools":["Bash","Read"]}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"list files"}]},"session_id":"sess-1","uuid":"msg-1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"ls -la"}},{"type":"tool_use","id":"tu-2","name":"Read","input":{"file_path":"/tmp/foo.go"}}]},"session_id":"sess-1","uuid":"msg-2","timestamp":"2024-01-15T10:00:00Z"}
{"type":"result","subtype":"success","session_id":"sess-1","cost_usd":0.01}
`)

	result, err := parseFromReader(data)
	require.NoError(t, err)
	assert.Equal(t, claude.FormatClaudeStreamJSON, result.Format)
	assert.Nil(t, result.CLIOut)
	// Stream-json now surfaces chat, system/init, and result/* rows alongside
	// real tool_use blocks.
	require.Len(t, result.ToolUses, 5)
	names := []string{
		result.ToolUses[0].Tool,
		result.ToolUses[1].Tool,
		result.ToolUses[2].Tool,
		result.ToolUses[3].Tool,
		result.ToolUses[4].Tool,
	}
	assert.Equal(t, []string{"SessionInit", "User", "Bash", "Read", "Result"}, names)
	assert.Equal(t, "ls -la", result.ToolUses[2].Input["command"])
	assert.Equal(t, "/tmp/foo.go", result.ToolUses[3].Input["file_path"])
}

func TestParseFromReader_ClaudeCLI(t *testing.T) {
	data := []byte(`{"result":"Hello world","session_id":"sess-abc","cost_usd":0.01,"duration_ms":1234,"num_turns":1,"usage":{"input_tokens":100,"output_tokens":50}}`)

	result, err := parseFromReader(data)
	require.NoError(t, err)
	assert.Equal(t, claude.FormatClaudeCLI, result.Format)
	assert.Empty(t, result.ToolUses)
	require.NotNil(t, result.CLIOut)
	assert.Equal(t, "Hello world", result.CLIOut.Result)
	assert.Equal(t, "sess-abc", result.CLIOut.SessionID)
	assert.Equal(t, 0.01, result.CLIOut.CostUSD)
	assert.Equal(t, 100, result.CLIOut.Usage.InputTokens)
	assert.Equal(t, 50, result.CLIOut.Usage.OutputTokens)
}

func TestParseFromReader_Empty(t *testing.T) {
	_, err := parseFromReader([]byte(""))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty input")
}

func TestParseFromReader_Unknown(t *testing.T) {
	_, err := parseFromReader([]byte(`{"foo":"bar"}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized stream format")
}

func TestFirstNonEmptyLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "hello\nworld", "hello"},
		{"leading empty lines", "\n\n  \nhello", "hello"},
		{"all empty", "\n\n  \n", ""},
		{"single line", "hello", "hello"},
		{"whitespace trimmed", "  hello  \nworld", "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstNonEmptyLine([]byte(tt.input))
			assert.Equal(t, tt.expected, string(got))
		})
	}
}

func TestParseFromReader_SkipsLeadingMetadata(t *testing.T) {
	// Newer Claude Code session JSONL files lead with metadata records
	// (file-history-snapshot, last-prompt, permission-mode, system/local_command)
	// that don't match any known format. parseFromReader must scan past them
	// to find the underlying stream format.
	data := []byte(`{"type":"file-history-snapshot","messageId":"x","snapshot":{}}
{"type":"last-prompt","leafUuid":"y","sessionId":"abc"}
{"type":"permission-mode","permissionMode":"plan","sessionId":"abc"}
{"sessionId":"abc","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"ls"}}]},"uuid":"1","timestamp":"2024-01-01T10:00:00Z"}
`)

	result, err := parseFromReader(data)
	require.NoError(t, err)
	assert.Equal(t, claude.FormatClaudeJSONL, result.Format)
	require.Len(t, result.ToolUses, 1)
	assert.Equal(t, "Bash", result.ToolUses[0].Tool)
}

func TestDetectStreamFormat(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectedFormat claude.StreamFormat
	}{
		{
			name:           "plain claude jsonl on first line",
			input:          `{"sessionId":"abc","message":{"role":"user","content":"hi"},"uuid":"1","timestamp":"t"}`,
			expectedFormat: claude.FormatClaudeJSONL,
		},
		{
			name: "metadata then claude jsonl",
			input: `{"type":"file-history-snapshot","snapshot":{}}
{"sessionId":"abc","message":{"role":"user","content":"hi"},"uuid":"1","timestamp":"t"}`,
			expectedFormat: claude.FormatClaudeJSONL,
		},
		{
			name: "metadata then stream-json",
			input: `{"type":"file-history-snapshot","snapshot":{}}
{"type":"assistant","session_id":"abc","message":{"role":"assistant","content":[]}}`,
			expectedFormat: claude.FormatClaudeStreamJSON,
		},
		{
			name:           "all metadata, no recognizable format",
			input:          `{"type":"file-history-snapshot","snapshot":{}}`,
			expectedFormat: claude.FormatUnknown,
		},
		{
			name:           "empty input",
			input:          "",
			expectedFormat: claude.FormatUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, _ := detectStreamFormat([]byte(tt.input))
			assert.Equal(t, tt.expectedFormat, format)
		})
	}
}

func TestRunHistoryFromReader_CLIOutput(t *testing.T) {
	data := []byte(`{"result":"test output","session_id":"s1","cost_usd":0.05,"duration_ms":500,"num_turns":2,"usage":{"input_tokens":200,"output_tokens":100}}`)

	result, err := runHistoryFromReader(data, HistoryOptions{})
	require.NoError(t, err)

	cliResult, ok := result.(CLIOutputResult)
	require.True(t, ok, "expected CLIOutputResult, got %T", result)
	assert.Equal(t, "test output", cliResult.Result)
	assert.Equal(t, "s1", cliResult.SessionID)
	assert.Equal(t, 0.05, cliResult.CostUSD)
	assert.Equal(t, 200, cliResult.Input)
	assert.Equal(t, 100, cliResult.Output)
}

func TestRunHistoryFromReader_ClaudeJSONL(t *testing.T) {
	data := []byte(`{"sessionId":"abc","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"echo hi"}}]},"uuid":"1","timestamp":"2024-01-01T10:00:00Z"}
`)

	result, err := runHistoryFromReader(data, HistoryOptions{Limit: 10})
	require.NoError(t, err)

	histResult, ok := result.(session.HistoryResult)
	require.True(t, ok, "expected HistoryResult, got %T", result)
	require.Len(t, histResult.Results, 1)
	assert.Equal(t, "Bash", histResult.Results[0].Tool)
}

func TestRunHistoryFromReader_ApprovedFilter(t *testing.T) {
	data := []byte(`{"sessionId":"abc","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"echo allowed"}}]},"uuid":"1","timestamp":"2024-01-01T10:00:00Z"}
{"sessionId":"abc","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-2","name":"Bash","input":{"command":"rm -rf /"}}]},"uuid":"2","timestamp":"2024-01-01T10:01:00Z"}
{"sessionId":"abc","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu-2","is_error":true,"content":"The user doesn't want to proceed with this tool use."}]},"uuid":"2b","timestamp":"2024-01-01T10:01:01Z"}
{"sessionId":"abc","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-3","name":"Read","input":{"file_path":"/tmp/x"}}]},"uuid":"3","timestamp":"2024-01-01T10:02:00Z"}
`)

	t.Run("no filter returns all", func(t *testing.T) {
		result, err := runHistoryFromReader(data, HistoryOptions{Limit: 100})
		require.NoError(t, err)
		histResult := result.(session.HistoryResult)
		assert.Equal(t, 3, histResult.Total)
	})

	t.Run("approved=true filters out denied", func(t *testing.T) {
		result, err := runHistoryFromReader(data, HistoryOptions{Limit: 100, Approved: "true"})
		require.NoError(t, err)
		histResult := result.(session.HistoryResult)
		assert.Equal(t, 2, histResult.Total)
		for _, r := range histResult.Results {
			assert.Equal(t, "✓", r.Approved)
		}
	})

	t.Run("approved=false shows only denied", func(t *testing.T) {
		result, err := runHistoryFromReader(data, HistoryOptions{Limit: 100, Approved: "false"})
		require.NoError(t, err)
		histResult := result.(session.HistoryResult)
		assert.Equal(t, 1, histResult.Total)
		assert.Contains(t, histResult.Results[0].Approved, "✗")
	})

	t.Run("text filter matches command content before limit", func(t *testing.T) {
		result, err := runHistoryFromReader(data, HistoryOptions{Limit: 1, TextFilter: "allowed"})
		require.NoError(t, err)
		histResult := result.(session.HistoryResult)
		assert.Equal(t, 1, histResult.Total)
		require.Len(t, histResult.Results, 1)
		assert.Contains(t, histResult.Results[0].Summary, "allowed")
	})
}

func TestHistoryTextFilterFromGlobal(t *testing.T) {
	assert.Equal(t, "arthas", HistoryTextFilterFromGlobal(" arthas "))
	assert.Equal(t, "cmd/arthas.go", HistoryTextFilterFromGlobal("cmd/arthas.go"))
	assert.Empty(t, HistoryTextFilterFromGlobal(`category == "test"`))
	assert.Empty(t, HistoryTextFilterFromGlobal(`summary.contains("arthas")`))
}

func TestRunHistoryFromReader_CategoryAliases(t *testing.T) {
	data := []byte(`{"timestamp":"2026-06-01T10:00:00Z","type":"session_meta","payload":{"id":"sess-1","cwd":"/p"}}
{"timestamp":"2026-06-01T10:00:01Z","type":"event_msg","payload":{"type":"agent_message","message":"Arthas assistant note"}}
{"timestamp":"2026-06-01T10:00:02Z","type":"response_item","payload":{"type":"function_call","name":"update_plan","call_id":"call-plan","arguments":"{\"plan\":[{\"step\":\"Inspect Arthas\",\"status\":\"completed\"}]}"}}`)

	result, err := runHistoryFromReader(data, HistoryOptions{
		Limit:      100,
		Categories: []string{"!explore", "!assistant"},
		TextFilter: "arthas",
	})
	require.NoError(t, err)

	histResult := result.(session.HistoryResult)
	require.Len(t, histResult.Results, 1)
	assert.Equal(t, "Task", histResult.Results[0].Tool)
	assert.Equal(t, "plan", histResult.Results[0].Category)
	assert.NotContains(t, histResult.Results[0].Summary, `{"plan"`)
}

func TestRunHistoryFromReader_CodexChatRowsAndChatCategoryFilter(t *testing.T) {
	data := []byte(`{"timestamp":"2026-07-08T11:19:57.028Z","type":"session_meta","payload":{"id":"sess-rollout","cwd":"/repo","cli_version":"0.143.0","model_provider":"openai"}}
{"timestamp":"2026-07-08T11:19:57.028Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}
{"timestamp":"2026-07-08T11:19:58.758Z","type":"turn_context","payload":{"model":"gpt-5.5","effort":"high"}}
{"timestamp":"2026-07-08T11:19:58.760Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}
{"timestamp":"2026-07-08T11:19:58.760Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}
{"timestamp":"2026-07-08T11:20:00.403Z","type":"event_msg","payload":{"type":"agent_message","message":"hello"}}
{"timestamp":"2026-07-08T11:20:00.403Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}
{"timestamp":"2026-07-08T11:20:00.435Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","duration_ms":3519}}
`)

	result, err := runHistoryFromReader(data, HistoryOptions{})
	require.NoError(t, err)
	histResult := result.(session.HistoryResult)
	require.Len(t, histResult.Results, 4)
	assert.Equal(t, []string{"TaskStarted", "User", "Assistant", "TaskComplete"}, []string{
		histResult.Results[0].Tool,
		histResult.Results[1].Tool,
		histResult.Results[2].Tool,
		histResult.Results[3].Tool,
	})
	for _, row := range histResult.Results {
		assert.Equal(t, "chat", row.Category)
	}

	filtered, err := runHistoryFromReader(data, HistoryOptions{Categories: []string{"!chat"}})
	require.NoError(t, err)
	assert.Empty(t, filtered.(session.HistoryResult).Results)
}
