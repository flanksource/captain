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
	if columns[5].MaxWidth != sessionInitialPromptWidth {
		t.Fatalf("initial prompt max width = %d, want %d", columns[5].MaxWidth, sessionInitialPromptWidth)
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
	if got := len([]rune(prompt)); got != sessionInitialPromptWidth {
		t.Fatalf("initial prompt width = %d, want %d: %q", got, sessionInitialPromptWidth, prompt)
	}
	if !strings.HasPrefix(prompt, "Improve the sessions table") || !strings.HasSuffix(prompt, "…") {
		t.Fatalf("initial prompt = %q", prompt)
	}
	for name, rendered := range map[string]string{"ansi": table.ANSI(), "markdown": table.Markdown()} {
		if !strings.Contains(rendered, "6522fe00-9a7") || !strings.Contains(rendered, "$1.25") {
			t.Fatalf("%s output missing compact identity/usage: %q", name, rendered)
		}
		if strings.Contains(rendered, longTail) || strings.Contains(rendered, secondLine) {
			t.Fatalf("%s output did not truncate the initial prompt: %q", name, rendered)
		}
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
