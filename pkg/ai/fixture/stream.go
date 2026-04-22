// ABOUTME: Parser for Claude Code stream-json output.
// ABOUTME: Produces a Summary with tokens, tool-call counts, cost, and duration.

package fixture

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

type Summary struct {
	SessionID  string
	Result     string
	Success    bool
	Error      string
	DurationMS float64
	CostUSD    float64
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
	ToolCalls  int
	MCPCalls   int
	BashCalls  int
	ToolCounts map[string]int
}

type streamEvent struct {
	Type       string          `json:"type,omitempty"`
	Subtype    string          `json:"subtype,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	Message    json.RawMessage `json:"message,omitempty"`
	Result     string          `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	CostUSD    float64         `json:"cost_usd,omitempty"`
	DurationMS float64         `json:"duration_ms,omitempty"`
	Usage      *streamUsage    `json:"usage,omitempty"`
}

type streamMessage struct {
	Content []streamContent `json:"content,omitempty"`
	Usage   *streamUsage    `json:"usage,omitempty"`
}

type streamContent struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name,omitempty"`
}

type streamUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

func ParseStream(data []byte) Summary {
	summary := Summary{ToolCounts: map[string]int{}}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.SessionID != "" {
			summary.SessionID = ev.SessionID
		}
		if len(ev.Message) > 0 {
			var msg streamMessage
			if err := json.Unmarshal(ev.Message, &msg); err == nil {
				if msg.Usage != nil {
					summary.Input += msg.Usage.InputTokens
					summary.Output += msg.Usage.OutputTokens
					summary.CacheRead += msg.Usage.CacheReadInputTokens
					summary.CacheWrite += msg.Usage.CacheCreationInputTokens
				}
				for _, use := range msg.Content {
					if use.Type != "tool_use" || use.Name == "" {
						continue
					}
					summary.ToolCalls++
					summary.ToolCounts[use.Name]++
					if strings.HasPrefix(use.Name, "mcp__") {
						summary.MCPCalls++
					}
					if use.Name == "Bash" {
						summary.BashCalls++
					}
				}
			}
		}
		if ev.Usage != nil {
			summary.Input = ev.Usage.InputTokens
			summary.Output = ev.Usage.OutputTokens
			summary.CacheRead = ev.Usage.CacheReadInputTokens
			summary.CacheWrite = ev.Usage.CacheCreationInputTokens
		}
		if ev.Type == "result" || ev.Result != "" || ev.Error != "" {
			if ev.Result != "" {
				summary.Result = ev.Result
			}
			if ev.Error != "" {
				summary.Error = ev.Error
			}
			if ev.DurationMS > 0 {
				summary.DurationMS = ev.DurationMS
			}
			if ev.CostUSD > 0 {
				summary.CostUSD = ev.CostUSD
			}
			if ev.Subtype == "success" && !ev.IsError {
				summary.Success = true
			}
		}
	}
	return summary
}
