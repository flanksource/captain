package session

import (
	"fmt"
	"path"
	"strings"

	"github.com/flanksource/captain/pkg/api"
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

	if items := s.TranscriptRows(); len(items) > 0 {
		t = t.NewLine().NewLine().Append("Transcript", "font-bold").
			NewLine().Add(TranscriptList(items))
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
		add("Cost", FormatCostEstimated(cost, s.Cost.ProviderCostUSD == 0))
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
