package cli

import "testing"

// TestClosestModel checks the "did you mean" typo core: a close misspelling of a
// known name suggests it; an exact match or a far-off (plausibly-valid) name does
// not. Candidates are fixed so the test is independent of catalog/registry state.
func TestClosestModel(t *testing.T) {
	known := []string{
		"anthropic/claude-sonnet-5", "claude-sonnet-5",
		"anthropic/claude-opus-4-8", "claude-opus-4-8",
		"openai/gpt-5.5", "gpt-5.5",
	}
	cases := []struct {
		model    string
		wantOK   bool
		wantBest string
	}{
		{"claud-sonnet-5", true, "claude-sonnet-5"},  // 1 edit — a typo
		{"claude-opus-4-9", true, "claude-opus-4-8"}, // 1 edit — a typo
		{"claude-sonnet-5", false, ""},               // exact base — known
		{"anthropic/claude-sonnet-5", false, ""},     // exact canonical id — known
		{"gpt-4o-mini-2024-07-18", false, ""},        // far from every candidate
		{"", false, ""},                              // empty
	}
	for _, tc := range cases {
		got, ok := closestModel(tc.model, known)
		if ok != tc.wantOK {
			t.Errorf("closestModel(%q) ok = %v, want %v (got %q)", tc.model, ok, tc.wantOK, got)
			continue
		}
		if tc.wantOK && got != tc.wantBest {
			t.Errorf("closestModel(%q) = %q, want %q", tc.model, got, tc.wantBest)
		}
	}
}
