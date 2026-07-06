package cli

import "testing"

// TestSuggestCatalogModel checks the "did you mean" typo detection: a close
// misspelling of a catalog model suggests it; an exact match or a far-off
// (plausibly-valid non-catalog) name does not.
func TestSuggestCatalogModel(t *testing.T) {
	cases := []struct {
		model    string
		wantOK   bool
		wantBase string // expected suggestion (base name), when wantOK
	}{
		{"claud-sonnet-5", true, "claude-sonnet-5"}, // 1 edit — a typo
		{"claude-sonnet-5", false, ""},              // exact catalog base — valid
		{"anthropic/claude-sonnet-5", false, ""},    // exact canonical id — valid
		{"gpt-4o-mini-2024-07-18", false, ""},       // far from catalog — plausibly valid
		{"", false, ""},                             // empty
	}
	for _, tc := range cases {
		got, ok := suggestCatalogModel(tc.model)
		if ok != tc.wantOK {
			t.Errorf("suggestCatalogModel(%q) ok = %v, want %v (got %q)", tc.model, ok, tc.wantOK, got)
			continue
		}
		if tc.wantOK && got != tc.wantBase {
			t.Errorf("suggestCatalogModel(%q) = %q, want %q", tc.model, got, tc.wantBase)
		}
	}
}
