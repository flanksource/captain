package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/clicky"
	"golang.org/x/term"
)

type lineEntry struct {
	Tool    string
	Command string
	Usage   string
	Time    string
	Denied  bool
}

func useStructuredOutput() bool {
	f := clicky.Flags
	if f.Table || f.JSON || f.YAML || f.CSV || f.HTML || f.Markdown || f.PDF || f.Format != "" {
		return true
	}
	_, _, err := term.GetSize(int(os.Stdout.Fd()))
	return err != nil
}

func useTableOutput() bool {
	f := clicky.Flags
	return f.Table || f.HTML || f.Markdown || f.PDF
}

func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 40 {
		return 120
	}
	return w
}

// lineRenderer prints tool history rows to an io.Writer, emitting a synthetic
// session-start banner whenever the (source, session, model, effort) key
// changes. Row content comes from session.TranscriptRow; this type only adds
// history's time, tool-name, usage, and session-boundary columns.
type lineRenderer struct {
	w         io.Writer
	width     int
	toolWidth int
	prevKey   sessionKey
	hasPrev   bool
}

func newLineRenderer(w io.Writer, toolWidth int) *lineRenderer {
	if toolWidth < 8 {
		toolWidth = 8
	}
	return &lineRenderer{w: w, width: termWidth(), toolWidth: toolWidth}
}

// Render emits a single tool row, preceded by a session header if the session
// boundary changed compared with the previous call.
func (r *lineRenderer) Render(t tools.Tool, compact bool) {
	if n := len(t.Name()); n > r.toolWidth {
		r.toolWidth = n
	}
	key := keyForTool(t)
	if !r.hasPrev || key != r.prevKey {
		r.printHeader(t)
		r.prevKey = key
		r.hasPrev = true
	}
	e := toLineEntry(session.NewTranscriptRow(t), compact, r.width, r.toolWidth)
	printLeftTo(r.w, e, r.toolWidth)
}

func (r *lineRenderer) printHeader(t tools.Tool) {
	fmt.Fprintln(r.w)
	fmt.Fprintln(r.w, "──", sessionHeaderText(t))
}

func renderLineByLine(tl []tools.Tool, compact bool) {
	maxName := 8
	for _, t := range tl {
		if n := len(t.Name()); n > maxName {
			maxName = n
		}
	}
	r := newLineRenderer(os.Stdout, maxName)
	for _, t := range tl {
		r.Render(t, compact)
	}
}

// sessionKey identifies a contiguous run of tool calls that share the same
// session, source, and model. A change in any field triggers a fresh
// session-start indicator in renderLineByLine.
type sessionKey struct {
	source    string
	sessionID string
	model     string
	effort    string
}

func keyForTool(t tools.Tool) sessionKey {
	base := t.Base()
	model := ""
	if len(base.Models) > 0 {
		model = base.Models[0].Model
	}
	return sessionKey{
		source:    base.Source,
		sessionID: base.SessionID,
		model:     model,
		effort:    base.ReasoningEffort,
	}
}

// sessionHeaderText composes the colorized "── ✨ Claude  model  reasoning=…  id  time"
// banner shown above a contiguous run of rows that share a session.
func sessionHeaderText(t tools.Tool) string {
	base := t.Base()
	source := base.Source
	if source == "" {
		source = "claude"
	}
	icon := "✨"
	if source == "codex" {
		icon = "🤖"
	}

	parts := []string{fmt.Sprintf("\x1b[1;36m%s %s\x1b[0m", icon, capitalize(source))}
	if len(base.Models) > 0 && base.Models[0].Model != "" {
		parts = append(parts, fmt.Sprintf("\x1b[35m%s\x1b[0m", base.Models[0].Model))
	}
	if base.ReasoningEffort != "" {
		parts = append(parts, fmt.Sprintf("\x1b[33mreasoning=%s\x1b[0m", base.ReasoningEffort))
	}
	if base.SessionID != "" {
		parts = append(parts, fmt.Sprintf("\x1b[90m%s\x1b[0m", shortSessionID(base.SessionID)))
	}
	if base.Timestamp != nil {
		parts = append(parts, fmt.Sprintf("\x1b[90m%s\x1b[0m", base.Timestamp.Format("2006-01-02 15:04")))
	}
	return strings.Join(parts, "  ")
}

func shortSessionID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func toLineEntry(row session.TranscriptRow, compact bool, width, toolWidth int) lineEntry {
	t := row.Tool()
	base := t.Base()
	name := t.Name()
	e := lineEntry{
		Tool:    name,
		Time:    base.PrettyTimestamp(),
		Denied:  base.Denied && name != "Plan" && name != "User",
		Command: row.Pretty().ANSI(),
	}
	if base.IsSidechain {
		e.Command = sidechainBadge(base) + e.Command
	}
	if compact {
		e.Command = firstLine(e.Command)
		e.Usage = base.Models.Pretty().ANSI()
		usageLen := 0
		if e.Usage != "" {
			usageLen = len(e.Usage) + 1
		}
		prefixLen := len(e.Time) + 1 + toolWidth + 1
		if e.Denied {
			prefixLen += 4
		}
		maxCmd := width - prefixLen - usageLen
		if maxCmd > 10 {
			e.Command = truncateANSI(e.Command, maxCmd)
		}
	}
	return e
}

// toolAgentLabel is the sub-agent attribution for a tool row: the task
// description if known, else the agent type. Empty for main-thread rows.
func toolAgentLabel(base *tools.BaseTool) string {
	if !base.IsSidechain {
		return ""
	}
	if base.AgentDesc != "" {
		return base.AgentDesc
	}
	if base.AgentType != "" {
		return base.AgentType
	}
	return "agent"
}

// sidechainBadge is the compact "↳ <agent>" prefix marking a transcript row that
// was produced by a nested sub-agent rather than the main thread.
func sidechainBadge(base *tools.BaseTool) string {
	label := base.AgentType
	if label == "" {
		label = "agent"
	}
	return clicky.Text("↳ ", "text-gray-500").Append(label+" ", "text-violet-400").ANSI()
}

func printLeftTo(w io.Writer, e lineEntry, toolWidth int) {
	timeStr := e.Time
	if timeStr != "" {
		timeStr += " "
	}
	marker := ""
	if e.Denied {
		marker = "✗ "
	}
	if e.Usage != "" {
		fmt.Fprintf(w, "%s%s%s %s %s\n", timeStr, marker, padRight(e.Tool, toolWidth), e.Command, e.Usage)
	} else {
		fmt.Fprintf(w, "%s%s%s %s\n", timeStr, marker, padRight(e.Tool, toolWidth), e.Command)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func truncateANSI(s string, maxVisible int) string {
	cutAt := maxVisible - 1
	if cutAt < 1 {
		cutAt = 1
	}
	visible := 0
	inEscape := false
	lastSafe := 0

	for i, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		visible++
		if visible >= cutAt {
			lastSafe = i
			break
		}
	}

	if visible < cutAt {
		return s
	}
	return s[:lastSafe] + "\x1b[0m…"
}
