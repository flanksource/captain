package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected StreamFormat
	}{
		{
			name:     "claude jsonl",
			input:    `{"sessionId":"abc-123","message":{"role":"user","content":"hello"},"uuid":"1","timestamp":"2024-01-01T00:00:00Z"}`,
			expected: FormatClaudeJSONL,
		},
		{
			name:     "codex session_meta",
			input:    `{"timestamp":"2024-01-01T00:00:00Z","type":"session_meta","payload":{"id":"sess-1","cwd":"/tmp"}}`,
			expected: FormatCodexJSONL,
		},
		{
			name:     "codex response_item",
			input:    `{"timestamp":"2024-01-01T00:00:00Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`,
			expected: FormatCodexJSONL,
		},
		{
			name:     "codex event_msg",
			input:    `{"timestamp":"2024-01-01T00:00:00Z","type":"event_msg","payload":{"type":"agent_message","message":"done"}}`,
			expected: FormatCodexJSONL,
		},
		{
			name:     "claude cli json",
			input:    `{"result":"Hello world","session_id":"sess-abc","cost_usd":0.01,"duration_ms":1234,"num_turns":1,"usage":{"input_tokens":100,"output_tokens":50}}`,
			expected: FormatClaudeCLI,
		},
		{
			name:     "unknown - empty object",
			input:    `{}`,
			expected: FormatUnknown,
		},
		{
			name:     "unknown - invalid json",
			input:    `not json at all`,
			expected: FormatUnknown,
		},
		{
			name:     "unknown - has sessionId but no message",
			input:    `{"sessionId":"abc"}`,
			expected: FormatUnknown,
		},
		{
			name:     "unknown - has result but no session_id",
			input:    `{"result":"hello"}`,
			expected: FormatUnknown,
		},
		{
			name:     "unknown - type is not a codex event type",
			input:    `{"type":"something_else","payload":{}}`,
			expected: FormatUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectFormat([]byte(tt.input))
			assert.Equal(t, tt.expected, got)
		})
	}
}
