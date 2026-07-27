package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/flanksource/clicky/api"
)

func TestSessionRecordTableProviderIsCompact(t *testing.T) {
	endedAt := time.Now().Add(-2 * time.Hour)
	longTail := strings.Repeat("x", 120)
	secondLine := "second line must not render"
	record := SessionRecord{
		ID:            "6522fe00-9a7c-4cee-a205-123456789abc",
		Source:        "codex",
		Project:       "/work/flanksource/captain",
		Model:         "gpt-5.6-sol",
		InitialPrompt: "Improve the sessions table " + longTail + "\n" + secondLine,
		EndedAt:       &endedAt,
		Tokens:        &SessionTokensWire{TotalTokens: 12_345},
		CostUSD:       1.25,
	}

	columns := record.Columns()
	wantColumns := []string{"age", "source", "project", "session", "model", "initial_prompt", "tokens"}
	if len(columns) != len(wantColumns) {
		t.Fatalf("columns = %+v", columns)
	}
	for i, want := range wantColumns {
		if columns[i].Name != want {
			t.Fatalf("column %d = %q, want %q", i, columns[i].Name, want)
		}
	}
	// The prompt column declares no width: it runs to whatever the terminal
	// leaves it, and the renderer truncates there.
	if columns[5].MaxWidth != 0 {
		t.Fatalf("initial prompt max width = %d, want no cap", columns[5].MaxWidth)
	}
	if !strings.Contains(columns[5].Style, "max-lines-[1]") {
		t.Fatalf("initial prompt style = %q", columns[5].Style)
	}

	row := record.Row()
	if columns[3].MaxWidth != sessionIDDisplayWidth {
		t.Fatalf("session max width = %d, want %d", columns[3].MaxWidth, sessionIDDisplayWidth)
	}
	if row["session"] != "6522fe00-9a7" {
		t.Fatalf("session = %q", row["session"])
	}
	if row["project"] != "captain" {
		t.Fatalf("project = %q", row["project"])
	}
	usage, ok := row["tokens"].(api.Textable)
	if !ok || !strings.Contains(usage.String(), "12.3K") || !strings.Contains(usage.String(), "$1.25") {
		t.Fatalf("tokens = %#v", row["tokens"])
	}
	if _, ok := row["age"].(api.Textable); !ok {
		t.Fatalf("age = %#v", row["age"])
	}

	table := api.NewTableFrom([]SessionRecord{record})
	prompt := table.Rows[0]["initial_prompt"].String()
	if strings.Contains(prompt, "\n") || strings.Contains(prompt, secondLine) {
		t.Fatalf("initial prompt is not one line: %q", prompt)
	}
	// The cell keeps its full width -- only the extra line is dropped. Capping
	// it here would truncate at a width the terminal knows nothing about.
	if !strings.Contains(prompt, longTail) {
		t.Fatalf("initial prompt was width-truncated before rendering: %q", prompt)
	}
	if !strings.HasPrefix(prompt, "Improve the sessions table") || !strings.HasSuffix(prompt, "…") {
		t.Fatalf("initial prompt = %q", prompt)
	}

	rendered := table.ANSI()
	if !strings.Contains(rendered, "6522fe00-9a7") || !strings.Contains(rendered, "$1.25") {
		t.Fatalf("ansi output missing compact identity/usage: %q", rendered)
	}
	if strings.Contains(rendered, longTail) || strings.Contains(rendered, secondLine) {
		t.Fatalf("ansi output did not truncate the initial prompt: %q", rendered)
	}
	if lines := strings.Count(strings.Trim(rendered, "\n"), "\n") + 1; lines != 5 {
		t.Fatalf("ansi output should be border/header/separator/row/border, got %d lines: %q", lines, rendered)
	}
}

func TestSessionRecordTableProviderHandlesSparseRows(t *testing.T) {
	row := (SessionRecord{ID: "short", CWD: "/work/project"}).Row()
	if row["session"] != "short" || row["project"] != "project" {
		t.Fatalf("row = %+v", row)
	}
	if _, ok := row["age"]; ok {
		t.Fatalf("zero age should be omitted: %+v", row)
	}
	if _, ok := row["tokens"]; ok {
		t.Fatalf("zero usage should be omitted: %+v", row)
	}
}
