package history

import (
	"encoding/json"
	"fmt"
)

type AgentMessage struct {
	Type       string         `json:"type"`
	SessionID  string         `json:"session_id,omitempty"`
	Model      string         `json:"model,omitempty"`
	Tools      []string       `json:"tools,omitempty"`
	Text       string         `json:"text,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Success    bool           `json:"success,omitempty"`
	Subtype    string         `json:"subtype,omitempty"`
	CostUSD    float64        `json:"cost_usd,omitempty"`
	NumTurns   int            `json:"num_turns,omitempty"`
	DurationMs int            `json:"duration_ms,omitempty"`
	Usage      *AgentUsage    `json:"usage,omitempty"`
	Errors     []string       `json:"errors,omitempty"`
	ResultText string         `json:"result_text,omitempty"`
	Message    string         `json:"message,omitempty"`
}

type AgentUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func ParseAgentLine(line []byte) (*AgentMessage, error) {
	line = trimWhitespace(line)
	if len(line) == 0 {
		return nil, nil
	}
	var msg AgentMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return &msg, nil
}

func trimWhitespace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\n' || b[start] == '\r') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\n' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
}

func Truncate(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}

func FormatToolUseSummary(tool string, input map[string]any) string {
	str := func(key string) string {
		if v, ok := input[key].(string); ok {
			return v
		}
		return ""
	}

	switch tool {
	case "Bash":
		return Truncate(str("command"), 80)
	case "Edit", "Read", "Write":
		return str("file_path")
	case "Grep":
		return fmt.Sprintf("%s %s", str("pattern"), str("path"))
	case "Glob":
		return str("pattern")
	case "Task":
		if desc := str("description"); desc != "" {
			return Truncate(desc, 80)
		}
		return Truncate(str("prompt"), 80)
	case "TodoWrite":
		return "TodoWrite"
	default:
		return tool
	}
}
