package session

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
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
		shared, rows := shareHistoryFileDir(rows)
		t = t.NewLine().NewLine().Append("History Files", "font-bold")
		if shared != "" {
			t = t.Append("  "+shared+"/", "text-muted")
		}
		t = t.NewLine().Add(historyFilesTable(rows))
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
	add("Counts", prettyCounts(s))
	add("Tokens", prettyTokens(s.Usage))
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

// prettyKV renders one summary row. The label is muted and the value takes the
// terminal's default foreground: styling both the same way (as an earlier
// revision did) collapsed the block into an unscannable wall of one gray.
func prettyKV(label, value string) clickyapi.Text {
	return clickyapi.Text{}.
		Append("  "+label+": ", "text-muted").
		Append(value, "")
}

// prettyCounts reports the session's real totals. When the transcript has been
// windowed the retained count is called out separately, so a bounded view never
// reads as the whole session.
func prettyCounts(s *Session) string {
	messages, events, toolCalls := len(s.Messages), len(s.Events), CountToolParts(s.Messages)
	shown := ""
	if w := s.Window; w != nil {
		shown = fmt.Sprintf(" (%d shown)", messages)
		messages, events, toolCalls = w.Messages, w.Events, w.ToolCalls
	}
	return fmt.Sprintf("%d messages%s, %d events, %d agents, %d tool calls",
		messages, shown, events, len(s.Agents), toolCalls)
}

// prettyTokens breaks the total down across every non-zero bucket, largest
// first. Cache traffic is usually the bulk of a session, so a breakdown that
// omits it cannot account for the total it reports.
func prettyTokens(u api.Usage) string {
	total := u.TotalTokens()
	if total == 0 {
		return ""
	}
	buckets := make([]string, 0, 5)
	for _, bucket := range []struct {
		label string
		count int
	}{
		{"cache read", u.CacheReadTokens},
		{"cache write", u.CacheWriteTokens},
		{"output", u.OutputTokens},
		{"reasoning", u.ReasoningTokens},
		{"input", u.InputTokens},
	} {
		if bucket.count > 0 {
			buckets = append(buckets, FormatTokens(bucket.count)+" "+bucket.label)
		}
	}
	return fmt.Sprintf("%s total (%s)", FormatTokens(total), strings.Join(buckets, ", "))
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

// shareHistoryFileDir lifts the directory every transcript shares out of the
// rows so the File column carries the distinguishing basename. Agent
// transcripts normally all live in one project directory, which otherwise
// truncated every row to the same unusable prefix. Returns an empty prefix and
// the rows untouched when the paths span directories.
func shareHistoryFileDir(rows []historyFileRow) (string, []historyFileRow) {
	if len(rows) == 0 {
		return "", rows
	}
	shared := path.Dir(rows[0].file)
	for _, row := range rows[1:] {
		shared = commonDirPrefix(shared, path.Dir(row.file))
	}
	if shared == "" || shared == "." || shared == "/" {
		return "", rows
	}
	trimmed := make([]historyFileRow, len(rows))
	for i, row := range rows {
		row.file = strings.TrimPrefix(row.file, shared+"/")
		trimmed[i] = row
	}
	return shared, trimmed
}

// commonDirPrefix returns the deepest directory a and b share, or "" when they
// only meet at the filesystem root. Agent transcripts nest under the root
// session's own directory, so an exact-match check would find nothing to lift.
func commonDirPrefix(a, b string) string {
	aParts, bParts := strings.Split(a, "/"), strings.Split(b, "/")
	shared := 0
	for shared < min(len(aParts), len(bParts)) && aParts[shared] == bParts[shared] {
		shared++
	}
	return strings.Join(aParts[:shared], "/")
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
		if redundantTranscriptEvents[e.Type] {
			continue
		}
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

// redundantTranscriptEvents are session-state checkpoints the provider rewrites
// on every turn. Their payload already appears verbatim as a message row, so
// rendering them costs one line per turn and carries no information. They stay
// in the model — JSON and YAML callers still receive them.
var redundantTranscriptEvents = map[string]bool{
	"last-prompt": true,
}

// transcriptList renders one row per entry, collapsing consecutive identical
// rows into a single row tagged with its repeat count. Providers rewrite
// state rows (titles, skill listings) on every turn, which otherwise produced
// dozens of byte-identical lines for a handful of distinct values.
func transcriptList(rows []transcriptRow) clickyapi.List {
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

// transcriptRowKey identifies a row for repeat collapsing. It has to include the
// detail: Pretty() is a bounded one-line preview, so two different long messages
// that happen to share an opening sentence render the same first line and would
// otherwise collapse into a single row tagged ×2.
func transcriptRowKey(text clickyapi.Text, detail clickyapi.Textable) string {
	if detail == nil {
		return text.String()
	}
	return text.String() + "\x00" + detail.String()
}

// countRepeatedRows returns how many rows starting at start carry the same key,
// always at least 1.
func countRepeatedRows(rows []transcriptRow, start int, key string) int {
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

// HTML keeps the one-line preview as the visible row and hangs the full body off
// a native <details>. The preview exists because a terminal row is one line; HTML
// has no such limit, and cutting the body to a terminal width there is what threw
// away most of every long assistant message.
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
