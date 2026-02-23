package claude

import (
	"encoding/json"
	"os"
)

type StreamFormat string

const (
	FormatClaudeJSONL StreamFormat = "claude-jsonl"
	FormatCodexJSONL  StreamFormat = "codex-jsonl"
	FormatClaudeCLI   StreamFormat = "claude-cli"
	FormatUnknown     StreamFormat = "unknown"
)

type ClaudeCLIOutput struct {
	Result       string           `json:"result,omitempty"`
	Structured   any              `json:"structured,omitempty"`
	Usage        *ClaudeCLIUsage  `json:"usage,omitempty"`
	Error        string           `json:"error,omitempty"`
	IsError      bool             `json:"is_error,omitempty"`
	SessionID    string           `json:"session_id,omitempty"`
	CostUSD      float64          `json:"cost_usd,omitempty"`
	DurationMS   float64          `json:"duration_ms,omitempty"`
	DurationAPI  float64          `json:"duration_api_ms,omitempty"`
	NumTurns     int              `json:"num_turns,omitempty"`
	TotalCostUSD float64          `json:"total_cost,omitempty"`
}

type ClaudeCLIUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// DetectFormat inspects the first non-empty line of input to determine the stream format.
func DetectFormat(firstLine []byte) StreamFormat {
	var m map[string]any
	if err := json.Unmarshal(firstLine, &m); err != nil {
		return FormatUnknown
	}

	// Claude JSONL: has "sessionId" and "message"
	if _, hasSessionID := m["sessionId"]; hasSessionID {
		if _, hasMessage := m["message"]; hasMessage {
			return FormatClaudeJSONL
		}
	}

	// Codex JSONL: has "type" with known event types
	if t, hasType := m["type"].(string); hasType {
		switch t {
		case "session_meta", "response_item", "event_msg":
			return FormatCodexJSONL
		}
	}

	// Claude CLI JSON: has "result" and "session_id" (underscore)
	if _, hasResult := m["result"]; hasResult {
		if _, hasSessionID := m["session_id"]; hasSessionID {
			return FormatClaudeCLI
		}
	}

	return FormatUnknown
}

func IsStdinPiped() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice == 0
}
