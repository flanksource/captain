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
