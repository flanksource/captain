package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
)

type TranscriptRow struct {
	order int
	ts    *time.Time
	tool  tools.Tool
}

func NewTranscriptRow(tool tools.Tool) TranscriptRow {
	return TranscriptRow{tool: tool}
}

func (r TranscriptRow) Pretty() clickyapi.Text {
	if r.tool == nil {
		return clicky.Text("")
	}
	return r.tool.Pretty()
}

func (r TranscriptRow) Detail() clickyapi.Textable {
	if r.tool == nil {
		return nil
	}
	return r.tool.Detail()
}

func (r TranscriptRow) Tool() tools.Tool { return r.tool }

func (s *Session) TranscriptRows() []TranscriptRow {
	if s == nil {
		return nil
	}
	rows := make([]TranscriptRow, 0, len(s.Messages)+len(s.Events))
	agents := sessionAgents(s)
	order := 0
	for _, m := range s.Messages {
		ts := messageTimestamp(m)
		agent := agents[messageAgentID(m)]
		for _, p := range m.Parts {
			tool := partTool(m, p, agent)
			if tool == nil {
				continue
			}
			order++
			rows = append(rows, TranscriptRow{order: order, ts: ts, tool: tool})
		}
	}
	for _, e := range s.Events {
		if redundantTranscriptEvents[e.Type] {
			continue
		}
		tool := eventTool(e)
		if tool == nil {
			continue
		}
		order++
		rows = append(rows, TranscriptRow{order: order, ts: e.Timestamp, tool: tool})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].ts, rows[j].ts
		switch {
		case left != nil && right != nil && !left.Equal(*right):
			return left.Before(*right)
		case left != nil && right == nil:
			return true
		case left == nil && right != nil:
			return false
		default:
			return rows[i].order < rows[j].order
		}
	})
	return rows
}

// redundantTranscriptEvents are session-state checkpoints the provider rewrites
// on every turn. Their payload already appears verbatim as a message row, so
// rendering them costs one line per turn and carries no information. They stay
// in the model — JSON and YAML callers still receive them.
var redundantTranscriptEvents = map[string]bool{
	"last-prompt": true,
}

// TranscriptList renders one row per entry, collapsing consecutive identical
// rows into a single row tagged with its repeat count.
func TranscriptList(rows []TranscriptRow) clickyapi.List {
	list := clicky.List()
	list.Unstyled = true
	list.MaxInline = 1
	for i := 0; i < len(rows); {
		if rows[i].tool == nil {
			i++
			continue
		}
		text := rows[i].tool.Pretty()
		detail := rows[i].tool.Detail()
		repeats := countRepeatedRows(rows, i, transcriptRowKey(text, detail))
		if repeats > 1 {
			text = text.Append(fmt.Sprintf("  ×%d", repeats), "text-muted")
		}
		list.Items = append(list.Items, transcriptListItem{text: text, detail: detail})
		i += repeats
	}
	return list
}

func transcriptRowKey(text clickyapi.Text, detail clickyapi.Textable) string {
	if detail == nil {
		return text.String()
	}
	return text.String() + "\x00" + detail.String()
}

func countRepeatedRows(rows []TranscriptRow, start int, key string) int {
	count := 1
	for next := start + 1; next < len(rows); next++ {
		tool := rows[next].tool
		if tool == nil || transcriptRowKey(tool.Pretty(), tool.Detail()) != key {
			break
		}
		count++
	}
	return count
}

type transcriptListItem struct {
	text   clickyapi.Text
	detail clickyapi.Textable
}

func (i transcriptListItem) String() string { return i.text.String() + "\n" }
func (i transcriptListItem) ANSI() string   { return i.text.ANSI() }

func (i transcriptListItem) HTML() string {
	if i.detail == nil {
		return i.text.HTML()
	}
	return "<details><summary>" + i.text.HTML() + "</summary>" + i.detail.HTML() + "</details>"
}

func (i transcriptListItem) Markdown() string {
	if i.detail == nil {
		return i.text.Markdown()
	}
	return i.text.Markdown() + "\n\n" + i.detail.Markdown()
}

func partTool(m Message, p Part, agent *Agent) tools.Tool {
	switch p.Type {
	case PartText:
		return newPrettyTool(prettyRole(m.Role), map[string]any{"text": p.Text}, m.Provenance, agent)
	case PartReasoning:
		return newPrettyTool("Reasoning", map[string]any{"text": p.Text}, m.Provenance, agent)
	case PartTool:
		name := strings.TrimSpace(p.ToolName)
		if name == "" {
			return nil
		}
		return newPrettyTool(name, toolPartInput(p), m.Provenance, agent)
	case PartFile:
		return newPrettyTool("File", map[string]any{
			"filename": p.Filename, "url": p.URL, "mediaType": p.MediaType,
		}, m.Provenance, agent)
	default:
		if strings.HasPrefix(p.Type, "tool-") {
			name := strings.TrimPrefix(p.Type, "tool-")
			if p.ToolName != "" {
				name = p.ToolName
			}
			return newPrettyTool(name, toolPartInput(p), m.Provenance, agent)
		}
		name := strings.TrimSpace(p.Type)
		if name == "" {
			return nil
		}
		return newPrettyTool(name, map[string]any{"text": p.Text}, m.Provenance, agent)
	}
}

func prettyRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	default:
		if role == "" {
			return "Message"
		}
		return strings.ToUpper(role[:1]) + role[1:]
	}
}

func newPrettyTool(rawTool string, input map[string]any, prov *Provenance, agent *Agent) tools.Tool {
	base := tools.BaseTool{RawTool: rawTool, Input: cleanToolInput(input)}
	if prov != nil {
		base.Timestamp = prov.Timestamp
		base.CWD = prov.CWD
		base.ProjectRoot = prov.CWD
		base.SessionID = prov.SessionID
		base.Source = prov.Source
		base.ReasoningEffort = prov.ReasoningEffort
	}
	if agent != nil && !agent.IsRoot {
		base.IsSidechain = true
		base.AgentID = agent.ID
		base.AgentType = agent.Type
		base.AgentDesc = agent.Desc
	}
	return tools.NewTool(base)
}

func eventTool(e Event) tools.Tool {
	input := make(map[string]any, len(e.Data)+3)
	for k, v := range e.Data {
		input[k] = v
	}
	input["event"] = firstNonEmpty(e.Type, "event")
	if e.Scope != "" {
		input["scope"] = e.Scope
	}
	if e.TurnID != "" {
		input["turn_id"] = e.TurnID
	}
	return tools.NewTool(tools.BaseTool{
		RawTool: tools.EventToolName(e.Type), Input: cleanToolInput(input), Timestamp: e.Timestamp,
	})
}

func toolPartInput(p Part) map[string]any {
	if m := rawJSONMap(p.Input); len(m) > 0 {
		return m
	}
	if len(p.Output) > 0 {
		return map[string]any{"output": rawJSONValue(p.Output)}
	}
	if p.State != "" {
		return map[string]any{"state": p.State}
	}
	if p.ToolCallID != "" {
		return map[string]any{"tool_call_id": p.ToolCallID}
	}
	return nil
}

func rawJSONMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		return m
	}
	return map[string]any{"value": compactWhitespace(string(raw))}
}

func rawJSONValue(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return compactWhitespace(string(raw))
}

func cleanToolInput(input map[string]any) map[string]any {
	clean := make(map[string]any, len(input))
	for k, v := range input {
		switch value := v.(type) {
		case string:
			if value == "" {
				continue
			}
		case nil:
			continue
		}
		clean[k] = v
	}
	return clean
}

func sessionAgents(s *Session) map[string]*Agent {
	agents := map[string]*Agent{"": nil}
	if s == nil {
		return agents
	}
	if s.Root != nil {
		agents[s.Root.ID] = s.Root
	}
	for _, a := range s.Agents {
		if a != nil {
			agents[a.ID] = a
		}
	}
	return agents
}

func messageTimestamp(m Message) *time.Time {
	if m.Provenance == nil {
		return nil
	}
	return m.Provenance.Timestamp
}

func messageAgentID(m Message) string {
	if m.AgentID != "" {
		return m.AgentID
	}
	if m.Provenance != nil {
		return m.Provenance.AgentID
	}
	return ""
}
