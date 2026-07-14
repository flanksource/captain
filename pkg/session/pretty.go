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

// Pretty renders the detailed session view used by `captain sessions get`.
// JSON/YAML callers still receive the full Session shape; this is the compact
// human-oriented view for terminals.
func (s *Session) Pretty() clickyapi.Text {
	if s == nil {
		return clickyapi.Text{Content: "(nil session)"}
	}

	title := "Session"
	if s.ID != "" {
		title += " " + shortPrettyID(s.ID)
	}
	t := clickyapi.Text{}.Append(title, "font-bold text-blue-600")
	if s.Source != "" {
		t = t.Append("  ", "").Append(strings.ToUpper(s.Source), "text-gray-500")
	}
	if s.Model != "" {
		t = t.Append("  ", "").Append(s.Model, "text-purple-600")
	}

	t = t.NewLine().NewLine().Append("Summary", "font-bold")
	for _, row := range sessionSummaryRows(s) {
		t = t.NewLine().Add(prettyKV(row.label, row.value))
	}

	if rows := sessionHistoryFileRows(s); len(rows) > 0 {
		t = t.NewLine().NewLine().Append("History Files", "font-bold").
			NewLine().Add(historyFilesTable(rows))
	}

	if items := sessionTranscriptItems(s); len(items) > 0 {
		t = t.NewLine().NewLine().Append("Transcript", "font-bold").
			NewLine().Add(transcriptList(items))
	}

	return t
}

type prettyKVRow struct {
	label string
	value string
}

func sessionSummaryRows(s *Session) []prettyKVRow {
	var rows []prettyKVRow
	add := func(label, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			rows = append(rows, prettyKVRow{label: label, value: value})
		}
	}

	add("ID", s.ID)
	add("Source", s.Source)
	add("Project", s.Project)
	add("CWD", s.CWD)
	add("History", firstNonEmpty(s.HistoryFile, agentHistoryFile(s.Root)))
	add("Model", s.Model)
	add("Provider", s.Provider)
	add("Version", s.Version)
	add("Git", prettyGit(s.Git))
	add("Started", prettyTime(s.StartedAt))
	add("Ended", prettyTime(s.EndedAt))
	add("Duration", prettyDuration(s.StartedAt, s.EndedAt))
	add("Counts", fmt.Sprintf("%d messages, %d events, %d agents, %d tool calls",
		len(s.Messages), len(s.Events), len(s.Agents), countSessionToolParts(s.Messages)))
	if tokens := s.Usage.TotalTokens(); tokens > 0 {
		add("Tokens", fmt.Sprintf("%s total (%s input, %s output)",
			FormatTokens(tokens), FormatTokens(s.Usage.InputTokens), FormatTokens(s.Usage.OutputTokens)))
	}
	if cost := s.Cost.Total(); cost > 0 {
		add("Cost", FormatCost(cost))
	}
	if s.Plan != nil {
		add("Plan", firstNonEmpty(s.Plan.Path, s.Plan.Slug, "inline"))
	}
	if len(s.Files.Read) > 0 || len(s.Files.Written) > 0 {
		add("Files", fmt.Sprintf("%d read, %d written", len(s.Files.Read), len(s.Files.Written)))
	}
	if s.Approvals.Approved > 0 || s.Approvals.Denied > 0 {
		add("Approvals", fmt.Sprintf("%d approved, %d denied", s.Approvals.Approved, s.Approvals.Denied))
	}
	return rows
}

func prettyKV(label, value string) clickyapi.Text {
	return clickyapi.Text{}.
		Append("  "+label+": ", "text-gray-500").
		Append(value, "text-muted")
}

type historyFileRow struct {
	scope string
	agent string
	file  string
}

func sessionHistoryFileRows(s *Session) []historyFileRow {
	if s == nil {
		return nil
	}
	rows := make([]historyFileRow, 0, len(s.Agents)+1)
	rootFile := firstNonEmpty(s.HistoryFile, agentHistoryFile(s.Root))
	if rootFile != "" {
		rows = append(rows, historyFileRow{scope: "root", agent: shortPrettyID(s.ID), file: rootFile})
	}
	for _, a := range s.Agents {
		if a == nil || a.IsRoot || a.HistoryFile == "" || a.HistoryFile == rootFile {
			continue
		}
		rows = append(rows, historyFileRow{scope: "agent", agent: prettyAgentName(a), file: a.HistoryFile})
	}
	return rows
}

func historyFilesTable(rows []historyFileRow) clickyapi.TextTable {
	table := clickyapi.TextTable{
		Headers: clickyapi.TextList{
			textValue("Scope"),
			textValue("Agent"),
			textValue("File"),
		},
		FieldNames: []string{"scope", "agent", "file"},
	}
	for _, row := range rows {
		table.Rows = append(table.Rows, clickyapi.TableRow{
			"scope": cellValue(row.scope),
			"agent": cellValue(row.agent),
			"file":  cellValue(row.file),
		})
	}
	return table
}

type transcriptRow struct {
	order int
	ts    *time.Time
	tool  tools.Tool
}

func sessionTranscriptItems(s *Session) []transcriptRow {
	if s == nil {
		return nil
	}
	rows := make([]transcriptRow, 0, len(s.Messages)+len(s.Events))
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
			rows = append(rows, transcriptRow{
				order: order,
				ts:    ts,
				tool:  tool,
			})
		}
	}
	for _, e := range s.Events {
		tool := eventTool(e)
		if tool == nil {
			continue
		}
		order++
		rows = append(rows, transcriptRow{
			order: order,
			ts:    e.Timestamp,
			tool:  tool,
		})
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

func transcriptList(rows []transcriptRow) clickyapi.List {
	list := clicky.List()
	list.Unstyled = true
	list.MaxInline = 1
	for _, row := range rows {
		if row.tool == nil {
			continue
		}
		list.Items = append(list.Items, transcriptListItem{text: row.tool.Pretty()})
	}
	return list
}

type transcriptListItem struct {
	text clickyapi.Text
}

func (i transcriptListItem) String() string   { return i.text.String() + "\n" }
func (i transcriptListItem) ANSI() string     { return i.text.ANSI() }
func (i transcriptListItem) HTML() string     { return i.text.HTML() }
func (i transcriptListItem) Markdown() string { return i.text.Markdown() }

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
			"filename":  p.Filename,
			"url":       p.URL,
			"mediaType": p.MediaType,
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
	base := tools.BaseTool{
		RawTool: rawTool,
		Input:   cleanToolInput(input),
	}
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
		RawTool:   tools.EventToolName(e.Type),
		Input:     cleanToolInput(input),
		Timestamp: e.Timestamp,
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
		if a == nil {
			continue
		}
		agents[a.ID] = a
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
