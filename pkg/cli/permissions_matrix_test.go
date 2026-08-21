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

// matrixCell reads one (setting, backend) cell out of the rendered tables, which
// is the grid a reader actually sees rather than the struct behind it.
func matrixCell(t *testing.T, text clickyapi.Text, setting, backend string) string {
	t.Helper()
	for _, table := range matrixTables(t, text) {
		field := ""
		for i, header := range table.Headers {
			if header.String() == backend && i < len(table.FieldNames) {
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
		t.Fatalf("no %q row for %s", setting, backend)
	}
	t.Fatalf("no column for backend %q", backend)
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
		backend string
		want    string
	}{
		// The four API backends never read permissions.mode; the editor offered
		// it on all of them anyway.
		{"API backends honour no posture", agent, "mode plan", "anthropic", "✗"},
		{"claude honours plan exactly", agent, "mode plan", "claude-cli", "✓"},
		// codex has no plan flag: the read-only sandbox is an approximation, and
		// it must not render the same as claude's native support.
		{"codex approximates plan", agent, "mode plan", "codex-agent", "~"},
		// dontAsk resolves to codex's read-only default — more prompting, not
		// less — so it is declared unsupported rather than approximated.
		{"codex cannot express dontAsk", agent, "mode dontAsk", "codex-cli", "✗"},

		// The provenance split: the same policy, the same backend, two answers.
		{"codex-agent cannot deny a built-in", agent, "tool deny", "codex-agent", "✗"},
		{"codex-agent can deny a caller tool", caller, "tool deny", "codex-agent", "✓"},
		{"claude-cli can deny a built-in", agent, "tool deny", "claude-cli", "✓"},
		{"claude-cli serves no caller tools", caller, "tool deny", "claude-cli", "✗"},
		{"ask needs a broker where caller tools exist", caller, "tool ask", "claude-agent", "?"},

		// The resource axis is asymmetric in both directions.
		{"claude-cli silences MCP", agent, "mcp disabled", "claude-cli", "✓"},
		{"claude-agent does not", agent, "mcp disabled", "claude-agent", "✗"},
		{"no backend enables MCP per server", agent, "mcp enabled", "codex-agent", "✗"},
		{"only claude-cli loads skills", agent, "skills enabled", "claude-cli", "✓"},
		{"nothing unloads a skill", agent, "skills disabled", "claude-cli", "✗"},
		{"plugins are inert", agent, "plugins enabled", "claude-cli", "✗"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matrixCell(t, tc.text, tc.setting, tc.backend); got != tc.want {
				t.Fatalf("%s / %s = %q, want %q", tc.backend, tc.setting, got, tc.want)
			}
		})
	}
}

// TestPermissionsMatrixCoversEveryBackend keeps the printed grid total. A backend
// missing from the output would read as "not applicable" rather than as an
// undeclared cell.
func TestPermissionsMatrixCoversEveryBackend(t *testing.T) {
	result := matrixResult(t, PermissionsMatrixOptions{})
	if len(result.Backends) != len(api.AllBackends()) {
		t.Fatalf("matrix covers %d backends, want %d", len(result.Backends), len(api.AllBackends()))
	}
	printed := map[string]bool{}
	for _, table := range matrixTables(t, result.Pretty()) {
		for _, header := range table.Headers {
			printed[header.String()] = true
		}
	}
	for _, backend := range api.AllBackends() {
		if !printed[string(backend)] {
			t.Errorf("backend %s has a declared row but no printed column", backend)
		}
	}
}

// TestPermissionsMatrixRejectsUnknownSelectors keeps a typo from silently
// producing an empty or full matrix — the same fail-loud rule the declaration
// itself follows.
func TestPermissionsMatrixRejectsUnknownSelectors(t *testing.T) {
	if _, err := RunPermissionsMatrix(PermissionsMatrixOptions{Backend: "claude"}); err == nil {
		t.Fatal("a backend name that is really a family should be refused")
	}
	if _, err := RunPermissionsMatrix(PermissionsMatrixOptions{Provenance: "builtin"}); err == nil {
		t.Fatal("an unknown provenance should be refused")
	}
}

// TestPermissionsMatrixNotesExplainEveryNonNativeCell pins the --notes contract:
// anything not honoured exactly must arrive with a reason a reader can act on.
func TestPermissionsMatrixNotesExplainEveryNonNativeCell(t *testing.T) {
	result := matrixResult(t, PermissionsMatrixOptions{Backend: "codex-agent", Notes: true})
	if len(result.Notes) == 0 {
		t.Fatal("codex-agent has approximated and unsupported cells but produced no caveats")
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
