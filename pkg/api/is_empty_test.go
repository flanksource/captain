package api

import (
	"testing"

	"github.com/flanksource/commons-db/shell"
)

// IsEmpty exists so a caller layering configuration never has to keep its own
// list of fields to check. The cases below are the ones a hand-written list gets
// wrong: fields far from the ones anyone thinks to enumerate, and the two blocks
// whose emptiness is a domain rule rather than a zero-value test.
func TestIsEmpty(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want bool
	}{
		{name: "zero spec", spec: Spec{}, want: true},
		{
			// The exact shape a hand-written six-field check reports as empty, so
			// a `.gavel.yaml` declaring only a permission mode is discarded rather
			// than applied.
			name: "permission mode only",
			spec: Spec{Permissions: Permissions{Mode: PermissionPlan}},
		},
		{name: "setup only", spec: Spec{Setup: &shell.Setup{Cwd: "/work"}}},
		{name: "session only", spec: Spec{SessionID: "sess-1"}},
		{name: "cli args only", spec: Spec{CLIArgs: map[string]any{"verbose": true}}},
		{name: "tool preferences only", spec: Spec{ToolPreferences: ToolPreferences{"billing": ToolModeAsk}}},
		{name: "model name only", spec: Spec{Model: Model{Name: "claude-sonnet-4-6"}}},
		{name: "prompt user only", spec: Spec{Prompt: Prompt{User: "do the thing"}}},

		// Tools and MCP marshal as derived views, so their emptiness is a domain
		// rule: a Tools block is empty iff it yields no policies, and an MCP block
		// iff it is not disabled and names nothing.
		{name: "tools deny only", spec: Spec{Permissions: Permissions{Tools: Tools{Deny: []string{"Bash"}}}}},
		{name: "mcp disabled", spec: Spec{Permissions: Permissions{MCP: MCP{Disabled: true}}}},
		{name: "empty tools block", spec: Spec{Permissions: Permissions{Tools: Tools{}}}, want: true},
		{name: "empty mcp block", spec: Spec{Permissions: Permissions{MCP: MCP{}}}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsEmpty(test.spec); got != test.want {
				t.Errorf("IsEmpty() = %v, want %v", got, test.want)
			}
		})
	}
}

// A Schema is a runtime Go type rather than configuration — it is tagged
// json:"-" — so it must not make an otherwise-empty spec look configured.
func TestIsEmpty_IgnoresRuntimeOnlyFields(t *testing.T) {
	if !IsEmpty(Spec{Prompt: Prompt{Schema: struct{ Answer string }{}}}) {
		t.Error("IsEmpty() = false for a spec carrying only a runtime-only field, want true")
	}
}
