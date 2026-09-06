package cli

import (
	"testing"

	clickyapi "github.com/flanksource/clicky/api"

	"github.com/flanksource/captain/pkg/api"
)

func matrixResult(t *testing.T, opts PermissionsMatrixOptions) PermissionsMatrixResult {
	t.Helper()
	out, err := RunPermissionsMatrix(opts)
	if err != nil {
		t.Fatalf("RunPermissionsMatrix(%+v): %v", opts, err)
	}
	result, ok := out.(PermissionsMatrixResult)
	if !ok {
		t.Fatalf("RunPermissionsMatrix returned %T, want PermissionsMatrixResult", out)
	}
	return result
}

// matrixTables collects every rendered table, since Pretty emits one per family.
func matrixTables(t *testing.T, text clickyapi.Text) []clickyapi.TextTable {
	t.Helper()
	var out []clickyapi.TextTable
	for _, child := range text.Children {
		if table, ok := child.(clickyapi.TextTable); ok {
			out = append(out, table)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no table child in %#v", text.Children)
	}
	return out
}

// matrixCell reads one (setting, runtime) cell out of the rendered tables, which
// is the grid a reader actually sees rather than the struct behind it. A runtime
// column is headed "<provider> <mode>" — the pair, never a single token.
func matrixCell(t *testing.T, text clickyapi.Text, setting, runtime string) string {
	t.Helper()
	for _, table := range matrixTables(t, text) {
		field := ""
		for i, header := range table.Headers {
			if header.String() == runtime && i < len(table.FieldNames) {
				field = table.FieldNames[i]
			}
		}
		if field == "" {
			continue
		}
		for _, row := range table.Rows {
			if row["setting"].String() == setting {
				return row[field].String()
			}
		}
		t.Fatalf("no %q row for %s", setting, runtime)
	}
	t.Fatalf("no column for runtime %q", runtime)
	return ""
}

// TestPermissionsMatrixCells pins the cells that carry a real finding, so a
// change in behaviour has to change a visible row rather than sliding through as
// an implementation detail.
func TestPermissionsMatrixCells(t *testing.T) {
	agent := matrixResult(t, PermissionsMatrixOptions{}).Pretty()
	caller := matrixResult(t, PermissionsMatrixOptions{Provenance: "caller"}).Pretty()

	cases := []struct {
		name    string
		text    clickyapi.Text
		setting string
		runtime string
		want    string
	}{
		// The api modes have no provider-native sandbox approval posture; the editor offered
		// it on all of them anyway.
		{"api modes honour no posture", agent, "mode plan", "anthropic api", "✗"},
		{"anthropic cli honours plan exactly", agent, "mode plan", "anthropic cli", "✓"},
		// openai has no plan flag: the read-only sandbox is an approximation, and
		// it must not render the same as anthropic's native support.
		{"openai agent approximates plan", agent, "mode plan", "openai agent", "~"},
		// dontAsk resolves to openai's read-only default — more prompting, not
		// less — so it is declared unsupported rather than approximated.
		{"openai cli cannot express dontAsk", agent, "mode dontAsk", "openai cli", "✗"},

		// The provenance split: the same policy, the same runtime, two answers.
		{"openai agent cannot deny a built-in", agent, "tool deny", "openai agent", "✗"},
		{"openai agent can deny a caller tool", caller, "tool deny", "openai agent", "✓"},
		{"anthropic cli can deny a built-in", agent, "tool deny", "anthropic cli", "✓"},
		{"anthropic cli serves no caller tools", caller, "tool deny", "anthropic cli", "✗"},
		{"ask needs a broker where caller tools exist", caller, "tool ask", "anthropic agent", "?"},

		// The resource axis is asymmetric in both directions.
		{"anthropic cli silences MCP", agent, "mcp disabled", "anthropic cli", "✓"},
		{"anthropic agent does not", agent, "mcp disabled", "anthropic agent", "✗"},
		{"no runtime enables MCP per server", agent, "mcp enabled", "openai agent", "✗"},
		{"only anthropic cli loads skills", agent, "skills enabled", "anthropic cli", "✓"},
		{"nothing unloads a skill", agent, "skills disabled", "anthropic cli", "✗"},
		{"plugins are inert", agent, "plugins enabled", "anthropic cli", "✗"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matrixCell(t, tc.text, tc.setting, tc.runtime); got != tc.want {
				t.Fatalf("%s / %s = %q, want %q", tc.runtime, tc.setting, got, tc.want)
			}
		})
	}
}

// TestPermissionsMatrixCoversEveryRuntime keeps the printed grid total. A runtime
// missing from the output would read as "not applicable" rather than as an
// undeclared cell.
func TestPermissionsMatrixCoversEveryRuntime(t *testing.T) {
	result := matrixResult(t, PermissionsMatrixOptions{})
	if len(result.Runtimes) != len(api.AllRuntimes()) {
		t.Fatalf("matrix covers %d runtimes, want %d", len(result.Runtimes), len(api.AllRuntimes()))
	}
	printed := map[string]bool{}
	for _, table := range matrixTables(t, result.Pretty()) {
		for _, header := range table.Headers {
			printed[header.String()] = true
		}
	}
	for _, r := range api.AllRuntimes() {
		if !printed[r.String()] {
			t.Errorf("runtime %v has a declared row but no printed column", r)
		}
	}
}

// TestPermissionsMatrixRejectsUnknownSelectors keeps a typo from silently
// producing an empty or full matrix — the same fail-loud rule the declaration
// itself follows.
func TestPermissionsMatrixRejectsUnknownSelectors(t *testing.T) {
	if _, err := RunPermissionsMatrix(PermissionsMatrixOptions{Provider: "acme"}); err == nil {
		t.Fatal("an unknown provider should be refused")
	}
	if _, err := RunPermissionsMatrix(PermissionsMatrixOptions{Mode: "sdk"}); err == nil {
		t.Fatal("an unknown runtime mode should be refused")
	}
	if _, err := RunPermissionsMatrix(PermissionsMatrixOptions{Provenance: "builtin"}); err == nil {
		t.Fatal("an unknown provenance should be refused")
	}
}

// TestPermissionsMatrixNotesExplainEveryNonNativeCell pins the --notes contract:
// anything not honoured exactly must arrive with a reason a reader can act on.
func TestPermissionsMatrixNotesExplainEveryNonNativeCell(t *testing.T) {
	result := matrixResult(t, PermissionsMatrixOptions{Provider: "openai", Mode: "agent", Notes: true})
	if len(result.Notes) == 0 {
		t.Fatal("openai agent has approximated and unsupported cells but produced no caveats")
	}
	byLabel := map[string]PermissionsMatrixNote{}
	for _, note := range result.Notes {
		byLabel[note.Setting] = note
	}
	for _, want := range []string{"mode dontAsk", "mode plan", "tool deny"} {
		note, ok := byLabel[want]
		if !ok {
			t.Errorf("no caveat for %q", want)
			continue
		}
		if note.Note == "" {
			t.Errorf("caveat for %q has no explanation", want)
		}
	}
}
