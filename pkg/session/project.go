package session

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/segmentio/encoding/json"
)

// ChatMessageMetadata is the per-message usage/cost envelope the live-chat
// surface (aichat SSE `finish` part, clicky-ui) rides on a message. Field names
// match the clicky-ui ChatMessageMetadata shape.
type ChatMessageMetadata struct {
	Usage         *api.Usage `json:"usage,omitempty"`
	CostBreakdown *api.Cost  `json:"costBreakdown,omitempty"`
	Cost          float64    `json:"cost,omitempty"`          // this session's USD
	ThreadCostUSD float64    `json:"threadCostUsd,omitempty"` // cumulative USD
	ContextTokens int        `json:"contextTokens,omitempty"` // last-turn input tokens
}

// ToUIMessages projects the session into the Vercel AI SDK v6 chat shape:
// tool_result parts are merged into their originating tool call (matched by
// ToolCallID) so each tool interaction is a single part, and the aggregate
// usage/cost rides on the returned metadata. The message ordering is preserved
// from the transcript.
func (s *Session) ToUIMessages() ([]Message, ChatMessageMetadata) {
	resultByID := map[string]Part{}
	for _, m := range s.Messages {
		for _, p := range m.Parts {
			if isToolResult(p) {
				resultByID[p.ToolCallID] = p
			}
		}
	}

	out := make([]Message, 0, len(s.Messages))
	for _, m := range s.Messages {
		parts := make([]Part, 0, len(m.Parts))
		for _, p := range m.Parts {
			if isToolResult(p) {
				// merged into its originating call below; drop the standalone row.
				continue
			}
			if isToolCall(p) {
				if res, ok := resultByID[p.ToolCallID]; ok {
					p.Output = res.Output
					p.State = res.State
				}
			}
			parts = append(parts, p)
		}
		if len(parts) == 0 {
			continue
		}
		nm := m
		nm.Parts = parts
		out = append(out, nm)
	}

	usage := s.Usage
	cost := s.Cost
	meta := ChatMessageMetadata{
		Usage:         &usage,
		CostBreakdown: &cost,
		Cost:          cost.Total(),
		ThreadCostUSD: cost.Total(),
	}
	return out, meta
}

// isToolCall reports whether p is a tool invocation (carries a tool name).
func isToolCall(p Part) bool {
	return p.Type == PartTool && p.ToolName != ""
}

// isToolResult reports whether p is a standalone tool result (a tool part with
// output but no tool name — the shape partsFromEntry emits for tool_result
// blocks).
func isToolResult(p Part) bool {
	return p.Type == PartTool && p.ToolName == "" && p.ToolCallID != ""
}

// Replay* are the JSONL-replay projection shapes consumed by the session viewer
// and clicky-ui's SessionViewer (which mirrors these field names). Note the
// snake_case inside ReplayToolUse/ReplayContent vs camelCase on ReplayEntry —
// this matches the on-disk Claude/Codex row shape those consumers expect.

type ReplayToolUse struct {
	Tool            string         `json:"tool,omitempty"`
	Input           map[string]any `json:"input,omitempty"`
	Timestamp       string         `json:"timestamp,omitempty"`
	CWD             string         `json:"cwd,omitempty"`
	SessionID       string         `json:"session_id,omitempty"`
	ToolUseID       string         `json:"tool_use_id,omitempty"`
	Source          string         `json:"source,omitempty"`
	Model           string         `json:"model,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Response        string         `json:"response,omitempty"`
}

type ReplayContent struct {
	Type     string         `json:"type,omitempty"`
	Text     string         `json:"text,omitempty"`
	Thinking string         `json:"thinking,omitempty"`
	Name     string         `json:"name,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
	ID       string         `json:"id,omitempty"`
}

type ReplayMessage struct {
	Role       string          `json:"role,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Content    []ReplayContent `json:"content,omitempty"`
}

type ReplayEntry struct {
	Type              string         `json:"type,omitempty"`
	ToolUse           *ReplayToolUse `json:"tool_use,omitempty"`
	Message           *ReplayMessage `json:"message,omitempty"`
	Timestamp         string         `json:"timestamp,omitempty"`
	CWD               string         `json:"cwd,omitempty"`
	SessionID         string         `json:"sessionId,omitempty"`
	UUID              string         `json:"uuid,omitempty"`
	IsAPIErrorMessage bool           `json:"isApiErrorMessage,omitempty"`
	APIErrorStatus    int            `json:"apiErrorStatus,omitempty"`
	Error             string         `json:"error,omitempty"`
}

// ToReplayEntries projects the session into the JSONL-replay row shape: one
// entry per message for its text/thinking blocks, and one entry per tool call
// (with the tool result merged in as Response). This is the inverse of the
// viewer's entry construction and the shape clicky-ui's SessionViewer consumes.
func (s *Session) ToReplayEntries() []ReplayEntry {
	responseByID := map[string]string{}
	for _, m := range s.Messages {
		for _, p := range m.Parts {
			if isToolResult(p) {
				responseByID[p.ToolCallID] = decodeOutputText(p.Output)
			}
		}
	}

	var out []ReplayEntry
	for _, m := range s.Messages {
		ts, cwd, sid, model := provenanceFields(m.Provenance)

		var content []ReplayContent
		for _, p := range m.Parts {
			switch p.Type {
			case PartText:
				content = append(content, ReplayContent{Type: "text", Text: p.Text})
			case PartReasoning:
				content = append(content, ReplayContent{Type: "thinking", Thinking: p.Text})
			}
		}
		if len(content) > 0 {
			out = append(out, ReplayEntry{
				Type:      m.Role,
				Message:   &ReplayMessage{Role: m.Role, Content: content},
				Timestamp: ts, CWD: cwd, SessionID: sid, UUID: m.ID,
			})
		}

		for _, p := range m.Parts {
			if !isToolCall(p) {
				continue
			}
			out = append(out, ReplayEntry{
				Type: "assistant",
				ToolUse: &ReplayToolUse{
					Tool:      p.ToolName,
					Input:     decodeInputMap(p.Input),
					Timestamp: ts, CWD: cwd, SessionID: sid,
					ToolUseID: p.ToolCallID,
					Source:    s.Source,
					Model:     model,
					Response:  responseByID[p.ToolCallID],
				},
				Timestamp: ts, CWD: cwd, SessionID: sid, UUID: p.ToolCallID,
			})
		}
	}
	return out
}

func provenanceFields(p *Provenance) (ts, cwd, sid, model string) {
	if p == nil {
		return "", "", "", ""
	}
	if p.Timestamp != nil {
		ts = p.Timestamp.Format("2006-01-02T15:04:05Z07:00")
	}
	return ts, p.CWD, p.SessionID, p.Model
}

func decodeInputMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// decodeOutputText renders a tool_result output as text: a JSON string is
// unquoted; anything else is returned as its raw JSON.
func decodeOutputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}
