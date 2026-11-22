package session

import (
	"strings"
	"testing"
)

func TestDeriveSessionTitleCollapsesAndTruncatesAtWordBoundary(t *testing.T) {
	prompt := "  Improve\n\tthe parser " + strings.Repeat("carefully ", 20)
	title := DeriveTitle(prompt)
	if strings.ContainsAny(title, "\n\t") {
		t.Fatalf("title contains uncollapsed whitespace: %q", title)
	}
	if got := len([]rune(strings.TrimSuffix(title, "…"))); got > derivedSessionTitleMaxRunes {
		t.Fatalf("title has %d runes, want at most %d: %q", got, derivedSessionTitleMaxRunes, title)
	}
	if !strings.HasSuffix(title, "…") {
		t.Fatalf("title = %q, want ellipsis", title)
	}
}

// Hook blocks mark injected prompt text with HTML comments, which a Markdown
// reader never sees, so a title must not show them either.
func TestDeriveSessionTitleDropsHTMLComments(t *testing.T) {
	prompt := "<!-- gavel:effort:effort -->\nThink hard.\n<!-- /gavel:effort:effort -->\n\n<!--\nmulti-line note\n-->\nFix the parser"
	if title := DeriveTitle(prompt); title != "Think hard. Fix the parser" {
		t.Fatalf("title = %q, want %q", title, "Think hard. Fix the parser")
	}
}
