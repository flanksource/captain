package session

import (
	"strings"
	"time"

	clickyapi "github.com/flanksource/clicky/api"
)

func prettyAgentName(a *Agent) string {
	if a == nil {
		return ""
	}
	if a.Desc != "" {
		return truncatePretty(a.Desc, 48)
	}
	if a.Type != "" {
		return a.Type
	}
	if a.ID != "" {
		return shortPrettyID(a.ID)
	}
	return "agent"
}

func agentHistoryFile(a *Agent) string {
	if a == nil {
		return ""
	}
	return a.HistoryFile
}

func countSessionToolParts(messages []Message) int {
	total := 0
	for _, m := range messages {
		for _, p := range m.Parts {
			if p.ToolName != "" && (p.Type == PartTool || strings.HasPrefix(p.Type, "tool-")) {
				total++
			}
		}
	}
	return total
}

func prettyGit(g GitState) string {
	parts := make([]string, 0, 3)
	if g.Branch != "" {
		parts = append(parts, g.Branch)
	}
	if g.Commit != "" {
		parts = append(parts, shortPrettyID(g.Commit))
	}
	if g.Worktree != "" {
		parts = append(parts, g.Worktree)
	}
	return strings.Join(parts, " ")
}

func prettyTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func prettyDuration(start, end *time.Time) string {
	if start == nil || end == nil || start.IsZero() || end.IsZero() {
		return ""
	}
	d := end.Sub(*start)
	if d < 0 {
		return ""
	}
	return d.Round(time.Second).String()
}

func shortPrettyID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truncatePretty(s string, max int) string {
	s = compactWhitespace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func compactWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func textValue(s string) clickyapi.Textable {
	return clickyapi.Text{Content: s}
}

func cellValue(s string) clickyapi.TypedValue {
	return clickyapi.TypedValue{Textable: clickyapi.Text{Content: s}}
}
