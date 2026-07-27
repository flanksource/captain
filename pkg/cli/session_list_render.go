package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky/api"
)

const sessionIDDisplayWidth = 12

// sessionInitialPromptStyle collapses the prompt to a single line but sets no
// width cap: the column runs to whatever the terminal leaves it and the renderer
// truncates there, rather than at a width picked here that the terminal knows
// nothing about.
const sessionInitialPromptStyle = "max-lines-[1] truncate-suffix"

var _ api.TableProvider = SessionRecord{}

// Columns keeps the historical session list compact while leaving the full
// SessionRecord available to JSON and detail consumers.
func (SessionRecord) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("age").Label("Age").Build(),
		api.Column("source").Label("Agent").MaxWidth(12).Build(),
		api.Column("project").Label("Project").MaxWidth(20).Build(),
		api.Column("session").Label("Session").MaxWidth(sessionIDDisplayWidth).Build(),
		api.Column("model").Label("Model").MaxWidth(24).Build(),
		api.Column("initial_prompt").Label("Initial Prompt").Style(sessionInitialPromptStyle).Build(),
		api.Column("tokens").Label("Tokens").Build(),
	}
}

func (r SessionRecord) Row() map[string]any {
	row := map[string]any{
		"source":         r.Source,
		"session":        sessionListID(r.ID),
		"model":          r.Model,
		"initial_prompt": sessionInitialPromptCell(r.InitialPrompt),
	}
	if project := sessionProjectName(r); project != "" {
		row["project"] = project
	}
	if age := sessionAge(r); age != nil {
		row["age"] = age
	}
	if usage := sessionUsageCell(r); usage != nil {
		row["tokens"] = usage
	}
	return row
}

func sessionListID(id string) string {
	if len(id) > sessionIDDisplayWidth {
		return id[:sessionIDDisplayWidth]
	}
	return id
}

func sessionInitialPromptCell(prompt string) api.Textable {
	return api.Text{}.Append(prompt, sessionInitialPromptStyle)
}

func sessionProjectName(r SessionRecord) string {
	project := strings.TrimSpace(r.Project)
	if project == "" {
		project = strings.TrimSpace(r.CWD)
	}
	if project == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(project))
}

func sessionAge(r SessionRecord) api.Textable {
	observedAt := sessionSortTime(r)
	if observedAt.IsZero() {
		return nil
	}
	return api.TimeAgo(&observedAt)
}

// sessionUsageCell renders the one compact usage value shared by session list
// and ps tables: "27.4M $13.61".
func sessionUsageCell(r SessionRecord) api.Textable {
	total := sessionTokenTotal(r)
	if total == 0 && r.CostUSD <= 0 {
		return nil
	}
	t := api.Text{}
	if total > 0 {
		t = t.Add(api.HumanNumber(total, "text-muted"))
	}
	if r.CostUSD > 0 {
		if total > 0 {
			t = t.Space()
		}
		t = t.Append(fmt.Sprintf("$%.2f", r.CostUSD), "text-green-600")
	}
	return t
}

func sessionTokenTotal(r SessionRecord) int64 {
	if r.Tokens == nil {
		return 0
	}
	if r.Tokens.TotalTokens > 0 {
		return int64(r.Tokens.TotalTokens)
	}
	return int64(r.Tokens.InputTokens + r.Tokens.OutputTokens + r.Tokens.CacheReadTokens + r.Tokens.CacheCreationTokens)
}
