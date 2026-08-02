package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
)

// TestSessionSummaryRows_TokensSurfaceEveryBucket guards the Tokens summary
// line against reporting a total that its own breakdown cannot account for.
// Cache traffic dominates real sessions, so omitting it made the line read as
// an arithmetic contradiction (70.0M total shown as 1.6K input + 477.6K output).
func TestSessionSummaryRows_TokensSurfaceEveryBucket(t *testing.T) {
	s := &Session{Usage: api.Usage{
		InputTokens:      1_646,
		OutputTokens:     477_608,
		CacheReadTokens:  66_479_263,
		CacheWriteTokens: 3_061_826,
	}}

	tokens := summaryRowValue(t, s, "Tokens")
	for _, want := range []string{"70.0M total", "66.5M cache read", "3.1M cache write", "477.6K output", "1.6K input"} {
		assert.Contains(t, tokens, want)
	}
}

// TestSessionSummaryRows_TokensOmitZeroBuckets keeps the line short for
// sessions that never touched cache or reasoning.
func TestSessionSummaryRows_TokensOmitZeroBuckets(t *testing.T) {
	s := &Session{Usage: api.Usage{InputTokens: 1_000, OutputTokens: 2_000}}

	tokens := summaryRowValue(t, s, "Tokens")
	assert.Contains(t, tokens, "3.0K total")
	assert.NotContains(t, tokens, "cache")
	assert.NotContains(t, tokens, "reasoning")
}

// TestSessionSummaryRows_CountsReportTotalsNotWindow guards against the
// summary describing the windowed slice as if it were the whole session: a
// bounded --limit must not make a 1210-message session report 200.
func TestSessionSummaryRows_CountsReportTotalsNotWindow(t *testing.T) {
	s := &Session{
		Messages: make([]Message, 200),
		Events:   make([]Event, 20),
		Window:   &TranscriptWindow{Messages: 1210, Events: 66, ToolCalls: 569},
	}

	counts := summaryRowValue(t, s, "Counts")
	assert.Contains(t, counts, "1210 messages")
	assert.Contains(t, counts, "200 shown")
	assert.Contains(t, counts, "66 events")
	assert.Contains(t, counts, "569 tool calls")
}

// TestSessionSummaryRows_CountsOmitShownWhenComplete keeps the line clean for
// unwindowed sessions.
func TestSessionSummaryRows_CountsOmitShownWhenComplete(t *testing.T) {
	s := &Session{Messages: make([]Message, 3), Events: make([]Event, 1)}

	counts := summaryRowValue(t, s, "Counts")
	assert.Contains(t, counts, "3 messages")
	assert.NotContains(t, counts, "shown")
}

// TestHistoryFilesRows_UsesCommonAncestorDir covers the real layout: the root
// transcript sits in the project directory while agent transcripts nest under
// <session-id>/subagents, so requiring an identical directory left every row
// truncating to the same useless prefix.
func TestHistoryFilesRows_UsesCommonAncestorDir(t *testing.T) {
	dir := "/Users/moshe/.claude/projects/-Users-moshe-go-src-github-com-flanksource-captain"
	rows := []historyFileRow{
		{scope: "root", agent: "ae95d3f5-930", file: dir + "/ae95d3f5.jsonl"},
		{scope: "agent", agent: "Explore widths", file: dir + "/ae95d3f5/subagents/agent-a.jsonl"},
	}

	shared, trimmed := shareHistoryFileDir(rows)
	assert.Equal(t, dir, shared)
	assert.Equal(t, "ae95d3f5.jsonl", trimmed[0].file)
	assert.Equal(t, "ae95d3f5/subagents/agent-a.jsonl", trimmed[1].file)
}

// TestPrettyKV_LabelAndValueRenderDistinctly is the regression guard for the
// summary block reading as a flat wall of one gray: label and value both
// resolved to #6b7280, so the intended hierarchy was invisible in ANSI.
func TestPrettyKV_LabelAndValueRenderDistinctly(t *testing.T) {
	ansi := prettyKV("Project", "captain").ANSI()

	label, value, ok := strings.Cut(ansi, "captain")
	if !assert.True(t, ok, "value missing from %q", ansi) {
		return
	}
	assert.NotEmpty(t, label, "label should carry its own styling")
	assert.NotEqual(t, sgrCodes(label), sgrCodes(value+"captain"),
		"label and value must not render in the same color: %q", ansi)
}

// TestHistoryFilesRows_ShareDirectoryPrefix verifies the File column carries
// the distinguishing basename rather than a common prefix that truncates every
// row to the same useless string.
func TestHistoryFilesRows_ShareDirectoryPrefix(t *testing.T) {
	dir := "/Users/moshe/.claude/projects/-Users-moshe-go-src-github-com-flanksource-captain"
	rows := []historyFileRow{
		{scope: "root", agent: "ae95d3f5-930", file: dir + "/root.jsonl"},
		{scope: "agent", agent: "Explore table widths", file: dir + "/agent-a.jsonl"},
	}

	shared, trimmed := shareHistoryFileDir(rows)
	assert.Equal(t, dir, shared)
	assert.Equal(t, []string{"root.jsonl", "agent-a.jsonl"}, []string{trimmed[0].file, trimmed[1].file})
}

// TestHistoryFilesRows_NoSharedDirKeepsFullPaths ensures cross-directory
// transcripts (worktrees, relocated sessions) stay unambiguous.
func TestHistoryFilesRows_NoSharedDirKeepsFullPaths(t *testing.T) {
	rows := []historyFileRow{
		{scope: "root", agent: "a", file: "/tmp/one/root.jsonl"},
		{scope: "agent", agent: "b", file: "/var/two/agent.jsonl"},
	}

	shared, trimmed := shareHistoryFileDir(rows)
	assert.Empty(t, shared)
	assert.Equal(t, rows, trimmed)
}

// TestTranscript_CollapsesConsecutiveDuplicateRows covers the dominant source
// of transcript bloat: Claude Code rewrites ai-title on every turn, so a long
// session emitted dozens of byte-identical rows conveying two distinct values.
func TestTranscript_CollapsesConsecutiveDuplicateRows(t *testing.T) {
	messages := make([]Message, 0, 6)
	for i := 0; i < 4; i++ {
		messages = append(messages, titleMessage("first-title", i))
	}
	for i := 4; i < 6; i++ {
		messages = append(messages, titleMessage("second-title", i))
	}

	lines := transcriptLines(&Session{Messages: messages})

	assert.Len(t, lines, 2, "expected one row per distinct title, got:\n%s", strings.Join(lines, "\n"))
	assert.Contains(t, lines[0], "first-title")
	assert.Contains(t, lines[0], "×4")
	assert.Contains(t, lines[1], "second-title")
	assert.Contains(t, lines[1], "×2")
}

// TestTranscript_KeepsSingleOccurrencesUnannotated ensures the ×N suffix marks
// real repetition rather than decorating every row.
func TestTranscript_KeepsSingleOccurrencesUnannotated(t *testing.T) {
	lines := transcriptLines(&Session{
		Messages: []Message{titleMessage("only-title", 0)},
		Events:   []Event{{Type: "task_started", Scope: "session", Timestamp: eventTime(1)}},
	})

	assert.Len(t, lines, 2)
	for _, line := range lines {
		assert.NotContains(t, line, "×")
	}
}

// TestTranscript_DropsRedundantStateCheckpoints covers last-prompt, which
// Claude Code rewrites per turn and whose payload already appears verbatim as
// the user message row.
func TestTranscript_DropsRedundantStateCheckpoints(t *testing.T) {
	s := &Session{
		Messages: []Message{{
			Role:       "user",
			Parts:      []Part{{Type: PartText, Text: "fix the widths"}},
			Provenance: &Provenance{Timestamp: eventTime(0)},
		}},
		Events: []Event{
			{Type: "last-prompt", Scope: "session", Timestamp: eventTime(1),
				Data: map[string]any{"lastPrompt": "fix the widths"}},
			{Type: "last-prompt", Scope: "session", Timestamp: eventTime(2),
				Data: map[string]any{"lastPrompt": "fix the widths"}},
		},
	}

	lines := transcriptLines(s)

	assert.Len(t, lines, 1, "only the user message should survive, got:\n%s", strings.Join(lines, "\n"))
	assert.Contains(t, lines[0], "fix the widths")
	assert.NotContains(t, strings.Join(lines, "\n"), "last-prompt")
}

// titleMessage mirrors how the reader lands an ai-title record: a SessionTitle
// tool part, not a session event.
func titleMessage(title string, offset int) Message {
	input, err := json.Marshal(map[string]any{"aiTitle": title})
	if err != nil {
		panic(err)
	}
	return Message{
		Role:       "assistant",
		Parts:      []Part{{Type: PartTool, ToolName: "SessionTitle", Input: input}},
		Provenance: &Provenance{Timestamp: eventTime(offset)},
	}
}

func eventTime(offsetSeconds int) *time.Time {
	ts := time.Date(2026, 7, 19, 3, 43, 5, 0, time.UTC).Add(time.Duration(offsetSeconds) * time.Second)
	return &ts
}

// transcriptLines renders just the transcript rows of a session, one per line.
func transcriptLines(s *Session) []string {
	rendered := strings.TrimSpace(TranscriptList(s.TranscriptRows()).String())
	if rendered == "" {
		return nil
	}
	return strings.Split(rendered, "\n")
}

func summaryRowValue(t *testing.T, s *Session, label string) string {
	t.Helper()
	for _, row := range sessionSummaryRows(s) {
		if row.label == label {
			return row.value
		}
	}
	t.Fatalf("summary row %q not found", label)
	return ""
}

// sgrCodes extracts the ANSI select-graphic-rendition parameters from a string
// so two spans can be compared on color alone.
func sgrCodes(s string) []string {
	var codes []string
	for {
		start := strings.Index(s, "\x1b[")
		if start < 0 {
			return codes
		}
		end := strings.Index(s[start:], "m")
		if end < 0 {
			return codes
		}
		codes = append(codes, s[start+2:start+end])
		s = s[start+end+1:]
	}
}
