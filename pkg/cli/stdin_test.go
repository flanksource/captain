package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude"
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
	assert.Equal(t, "CodexCommand", result.ToolUses[0].Tool)
	assert.Equal(t, "echo hello", result.ToolUses[0].Input["command"])
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
	require.Len(t, result.ToolUses, 2)
	assert.Equal(t, "Bash", result.ToolUses[0].Tool)
	assert.Equal(t, "ls -la", result.ToolUses[0].Input["command"])
	assert.Equal(t, "Read", result.ToolUses[1].Tool)
	assert.Equal(t, "/tmp/foo.go", result.ToolUses[1].Input["file_path"])
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

	histResult, ok := result.(HistoryResult)
	require.True(t, ok, "expected HistoryResult, got %T", result)
	require.Len(t, histResult.Results, 1)
	assert.Equal(t, "Bash", histResult.Results[0].Tool)
}
