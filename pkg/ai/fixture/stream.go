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
	Type  string          `json:"type,omitempty"`
	Name  string          `json:"name,omitempty"`
	Text  string          `json:"text,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type streamUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// Event is a structured view of one stream-json line, exposed so callers can
// render progress in real time while the run is still in flight.
type Event struct {
	Type      string
	Subtype   string
	SessionID string
	IsError   bool

	// For assistant messages: the content blocks (text + tool_use).
	Content []streamContent
	// Raw usage reported on the message (if any).
	MessageUsage *streamUsage

	// For result events.
	Result     string
	Error      string
	DurationMS float64
	CostUSD    float64
	Usage      *streamUsage
}

func ParseLine(line []byte) (Event, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Event{}, false
	}
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return Event{}, false
	}
	out := Event{
		Type:       ev.Type,
		Subtype:    ev.Subtype,
		SessionID:  ev.SessionID,
		IsError:    ev.IsError,
		Result:     ev.Result,
		Error:      ev.Error,
		DurationMS: ev.DurationMS,
		CostUSD:    ev.CostUSD,
		Usage:      ev.Usage,
	}
	if len(ev.Message) > 0 {
		var msg streamMessage
		if err := json.Unmarshal(ev.Message, &msg); err == nil {
			out.Content = msg.Content
			out.MessageUsage = msg.Usage
		}
	}
	return out, true
}

func (s *Summary) Apply(ev Event) {
	if s.ToolCounts == nil {
		s.ToolCounts = map[string]int{}
	}
	if ev.SessionID != "" {
		s.SessionID = ev.SessionID
	}
	if ev.MessageUsage != nil {
		s.Input += ev.MessageUsage.InputTokens
		s.Output += ev.MessageUsage.OutputTokens
		s.CacheRead += ev.MessageUsage.CacheReadInputTokens
		s.CacheWrite += ev.MessageUsage.CacheCreationInputTokens
	}
	for _, use := range ev.Content {
		if use.Type != "tool_use" || use.Name == "" {
			continue
		}
		s.ToolCalls++
		s.ToolCounts[use.Name]++
		if strings.HasPrefix(use.Name, "mcp__") {
			s.MCPCalls++
		}
		if use.Name == "Bash" {
			s.BashCalls++
		}
	}
	if ev.Usage != nil {
		s.Input = ev.Usage.InputTokens
		s.Output = ev.Usage.OutputTokens
		s.CacheRead = ev.Usage.CacheReadInputTokens
		s.CacheWrite = ev.Usage.CacheCreationInputTokens
	}
	if ev.Type == "result" || ev.Result != "" || ev.Error != "" {
		if ev.Result != "" {
			s.Result = ev.Result
		}
		if ev.Error != "" {
			s.Error = ev.Error
		}
		if ev.DurationMS > 0 {
			s.DurationMS = ev.DurationMS
		}
		if ev.CostUSD > 0 {
			s.CostUSD = ev.CostUSD
		}
		if ev.Subtype == "success" && !ev.IsError {
			s.Success = true
		}
	}
}

func ParseStream(data []byte) Summary {
	summary := Summary{ToolCounts: map[string]int{}}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		ev, ok := ParseLine(scanner.Bytes())
		if !ok {
			continue
		}
		summary.Apply(ev)
	}
	return summary
}
