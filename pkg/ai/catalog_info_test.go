package ai

import "testing"

func TestBackendToProvider(t *testing.T) {
	cases := map[Backend]string{
		BackendAnthropic:   "anthropic",
		BackendOpenAI:      "openai",
		BackendGemini:      "googleai",
		BackendDeepSeek:    "deepseek",
		BackendClaudeAgent: "anthropic",
		BackendCodexCLI:    "openai",
	}
	for b, want := range cases {
		if got := BackendToProvider(b); got != want {
			t.Fatalf("BackendToProvider(%q) = %q, want %q", b, got, want)
		}
	}
}

// TestCatalogInfoMatchesCatalog asserts the web JSON mirrors the catalog 1:1 in
// order and field values, and that API-model "configured" tracks the provider
// set passed by the caller (the contract the web client + stored threads rely
// on). Agent-model configured depends on local binaries and is not asserted.
func TestCatalogInfoMatchesCatalog(t *testing.T) {
	configured := []string{"anthropic"}
	info := CatalogInfo(configured)

	models := Catalog()
	if len(info) != len(models) {
		t.Fatalf("CatalogInfo len = %d, want %d", len(info), len(models))
	}
	for i, m := range models {
		got := info[i]
		if got.ID != m.ID {
			t.Fatalf("row %d id = %q, want %q", i, got.ID, m.ID)
		}
		if got.Provider != BackendToProvider(m.Backend) {
			t.Fatalf("row %d provider = %q, want %q", i, got.Provider, BackendToProvider(m.Backend))
		}
		if got.Label != m.Label || got.Reasoning != m.Reasoning || got.ContextWindow != m.ContextWindow {
			t.Fatalf("row %d field mismatch: %+v vs %+v", i, got, m)
		}
		if !m.IsAgent() {
			want := m.Backend == BackendAnthropic
			if got.Configured != want {
				t.Fatalf("row %d (%s) configured = %v, want %v", i, m.ID, got.Configured, want)
			}
		}
	}
}
