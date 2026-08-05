// ABOUTME: Parser for Claude Code stream-json output.
// ABOUTME: Produces a Summary with tokens, tool-call counts, cost, and duration.

package fixture

import (
	"bufio"
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

type Summary struct {
	SessionID       string
	Result          string
	Success         bool
	Error           string
	DurationMS      float64
	CostUSD         float64
	Input           int
	Output          int
	CacheRead       int
	CacheWrite      int
	ToolCalls       int
	MCPCalls        int
	BashCalls       int
	KubectlCalls    int
	KubectlAPICalls int
	KubectlAPILog   []KubectlAPIEntry
	MCPAPICalls     int
	MCPAPILog       []MCPAPIEntry
	ToolCallLog     []ToolCallEntry
	ToolCounts      map[string]int

	// usageFromResult records that the token counts came from the stream's
	// result line — the provider's own total — rather than being rebuilt from
	// per-message usage.
	usageFromResult bool
	// responses deduplicates the per-content-block lines one response spans,
	// for streams that report no result usage. See api.ResponseSet.
	responses api.ResponseSet
}

// UsageFromResult reports whether the token counts are the provider's reported
// total rather than a per-message reconstruction.
func (s *Summary) UsageFromResult() bool { return s.usageFromResult }

func firstNonZeroCost(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

type streamEvent struct {
	Type      string          `json:"type,omitempty"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	Result    string          `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// Two real producers spell the result's cost differently: the claude CLI's
	// stream-json result line reports total_cost_usd, while the claude-agent
	// SDK's turn-done notification reports cost_usd. Accept both.
	CostUSD      float64      `json:"cost_usd,omitempty"`
	TotalCostUSD float64      `json:"total_cost_usd,omitempty"`
	DurationMS   float64      `json:"duration_ms,omitempty"`
	Usage        *streamUsage `json:"usage,omitempty"`
}

type streamMessage struct {
	// ID identifies the API response; a response spanning several content
	// blocks is written as several lines that all repeat the same usage.
	ID      string          `json:"id,omitempty"`
	Content []streamContent `json:"content,omitempty"`
	Usage   *streamUsage    `json:"usage,omitempty"`
}

type streamContent struct {
	Type      string          `json:"type,omitempty"`
	Name      string          `json:"name,omitempty"`
	Text      string          `json:"text,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ID        string          `json:"id,omitempty"`          // assistant tool_use id
	ToolUseID string          `json:"tool_use_id,omitempty"` // user tool_result link
	Content   json.RawMessage `json:"content,omitempty"`     // tool_result body (string or [{type,text}])
}

// ToolResultText extracts the text body from a tool_result content field, which
// the Claude CLI emits either as a plain string or as an array of content blocks.
func ToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		var parts []string
		for _, p := range arr {
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
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
	// MessageID identifies the API response the message belongs to.
	MessageID string

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
		CostUSD:    firstNonZeroCost(ev.TotalCostUSD, ev.CostUSD),
		Usage:      ev.Usage,
	}
	if len(ev.Message) > 0 {
		var msg streamMessage
		if err := json.Unmarshal(ev.Message, &msg); err == nil {
			out.Content = msg.Content
			out.MessageUsage = msg.Usage
			out.MessageID = msg.ID
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
	if ev.MessageUsage != nil && !s.usageFromResult && s.responses.First(ev.MessageID) {
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
			if isKubectlCommand(bashCommandFrom(use.Input)) {
				s.KubectlCalls++
			}
		}
	}
	// The result line's usage is the whole invocation's total, not the final
	// turn's — pkg/ai/provider/claude_cli.go reads it that way, and its test
	// pins a verbatim CLI result line proving it. So it replaces the per-message
	// accumulation rather than adding to it: a reported total is exact, where a
	// total rebuilt from per-message lines has to be deduplicated and can still
	// drift. The accumulation above remains the fallback for streams that report
	// no result usage.
	if ev.Usage != nil {
		s.Input = ev.Usage.InputTokens
		s.Output = ev.Usage.OutputTokens
		s.CacheRead = ev.Usage.CacheReadInputTokens
		s.CacheWrite = ev.Usage.CacheCreationInputTokens
		s.usageFromResult = true
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

func bashCommandFrom(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var decoded struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &decoded); err != nil {
		return ""
	}
	return decoded.Command
}

var kubectlMatcher = regexp.MustCompile(`(?:^|[\s;|&(])kubectl(?:\s|$)`)

func isKubectlCommand(cmd string) bool {
	if cmd == "" {
		return false
	}
	return kubectlMatcher.MatchString(cmd)
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
