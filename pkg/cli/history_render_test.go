package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude/tools"
)

func TestKeyForTool_ChangesAndRepeats(t *testing.T) {
	mk := func(source, sessionID, model, effort string) tools.Tool {
		base := tools.BaseTool{
			SessionID:       sessionID,
			Source:          source,
			ReasoningEffort: effort,
		}
		if model != "" {
			base.Models = tools.Models{{Model: model}}
		}
		return tools.NewTool(base)
	}

	a := keyForTool(mk("claude", "S1", "claude-opus-4-7", ""))
	b := keyForTool(mk("claude", "S1", "claude-opus-4-7", ""))
	if a != b {
		t.Errorf("identical sessions should compare equal, got %v vs %v", a, b)
	}

	cases := []struct {
		name  string
		left  tools.Tool
		right tools.Tool
	}{
		{"different source", mk("claude", "S1", "m", "e"), mk("codex", "S1", "m", "e")},
		{"different session id", mk("claude", "S1", "m", "e"), mk("claude", "S2", "m", "e")},
		{"different model", mk("claude", "S1", "m1", "e"), mk("claude", "S1", "m2", "e")},
		{"different effort", mk("claude", "S1", "m", "low"), mk("claude", "S1", "m", "high")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if keyForTool(tc.left) == keyForTool(tc.right) {
				t.Errorf("%s should yield different keys", tc.name)
			}
		})
	}
}

func TestCapitalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"a", "A"},
		{"claude", "Claude"},
		{"codex", "Codex"},
	}
	for _, tc := range tests {
		if got := capitalize(tc.in); got != tc.want {
			t.Errorf("capitalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShortSessionID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"abcd", "abcd"},
		{"6522fe00-9a7c-4cee", "6522fe00"},
	}
	for _, tc := range tests {
		if got := shortSessionID(tc.in); got != tc.want {
			t.Errorf("shortSessionID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
