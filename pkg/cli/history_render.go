package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/claude/tools"
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

func renderLineByLine(tl []tools.Tool, compact bool) {
	w := termWidth()
	toolWidth := 8
	for _, t := range tl {
		if n := len(t.Name()); n > toolWidth {
			toolWidth = n
		}
	}

	var prevKey sessionKey
	for i, t := range tl {
		key := keyForTool(t)
		if i == 0 || key != prevKey {
			printSessionHeader(t)
			prevKey = key
		}
		e := toLineEntry(t, compact, w, toolWidth)
		printLeft(e, toolWidth)
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

func printSessionHeader(t tools.Tool) {
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

	fmt.Println()
	fmt.Println("──", strings.Join(parts, "  "))
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

func toLineEntry(t tools.Tool, compact bool, width, toolWidth int) lineEntry {
	base := t.Base()
	name := t.Name()
	e := lineEntry{
		Tool:    name,
		Time:    base.PrettyTimestamp(),
		Denied:  base.Denied && name != "Plan" && name != "User",
		Command: t.Pretty().ANSI(),
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

func printLeft(e lineEntry, toolWidth int) {
	timeStr := e.Time
	if timeStr != "" {
		timeStr += " "
	}
	marker := ""
	if e.Denied {
		marker = "✗ "
	}
	if e.Usage != "" {
		fmt.Printf("%s%s%s %s %s\n", timeStr, marker, padRight(e.Tool, toolWidth), e.Command, e.Usage)
	} else {
		fmt.Printf("%s%s%s %s\n", timeStr, marker, padRight(e.Tool, toolWidth), e.Command)
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
