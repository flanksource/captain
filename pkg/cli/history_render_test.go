package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
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

func TestLastSessionTools_TrimsToFinalSession(t *testing.T) {
	mk := func(session, model string) tools.Tool {
		return tools.NewTool(tools.BaseTool{
			SessionID: session,
			Source:    "claude",
			Models:    tools.Models{{Model: model}},
		})
	}

	cases := []struct {
		name string
		in   []tools.Tool
		want int
	}{
		{name: "empty", in: nil, want: 0},
		{name: "single", in: []tools.Tool{mk("S1", "m")}, want: 1},
		{name: "all same session",
			in:   []tools.Tool{mk("S1", "m"), mk("S1", "m"), mk("S1", "m")},
			want: 3},
		{name: "two sessions keeps trailing one",
			in:   []tools.Tool{mk("S1", "m"), mk("S1", "m"), mk("S2", "m"), mk("S2", "m")},
			want: 2},
		{name: "model change splits session",
			in:   []tools.Tool{mk("S1", "m1"), mk("S1", "m2"), mk("S1", "m2")},
			want: 2},
		{name: "three sessions keeps only last",
			in:   []tools.Tool{mk("S1", "m"), mk("S2", "m"), mk("S3", "m"), mk("S3", "m")},
			want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lastSessionTools(tc.in)
			if len(got) != tc.want {
				t.Errorf("len = %d, want %d", len(got), tc.want)
			}
		})
	}
}

func TestRenderResultEvent_RoutesThroughLineRenderer(t *testing.T) {
	var buf bytes.Buffer
	r := newLineRenderer(&buf, 8)
	renderResultEvent(r, ai.Event{
		Kind:    ai.EventResult,
		Model:   "claude-opus-4-7",
		Success: true,
		CostUSD: 0.0123,
		Usage:   &ai.Usage{InputTokens: 100, OutputTokens: 200},
		Input:   map[string]any{"num_turns": float64(3), "duration_ms": float64(1500)},
	})

	out := buf.String()
	for _, want := range []string{"result", "$0.0123", "turns=3", "1.5s"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestRenderResultEvent_FailureMarksError(t *testing.T) {
	var buf bytes.Buffer
	r := newLineRenderer(&buf, 8)
	renderResultEvent(r, ai.Event{
		Kind:    ai.EventResult,
		Success: false,
		Error:   "timeout",
	})
	if !strings.Contains(buf.String(), "ERROR") {
		t.Errorf("failure result must include ERROR marker, got: %q", buf.String())
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
