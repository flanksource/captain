package ai

import "testing"

// A catalog row's provider is its family key, the same token every surface
// speaks. It used to be a composite adapter id run through a translation, which
// is how Google — whose key is "google" but whose catalog prefix is "googleai" —
// ended up under two different names depending on which side you asked.
func TestModelInfoProviderIsTheProviderKey(t *testing.T) {
	cases := map[*ModelProvider]string{
		Anthropic: "anthropic",
		OpenAI:    "openai",
		Google:    "google",
		DeepSeek:  "deepseek",
	}
	for p, want := range cases {
		if got := providerName(p); got != want {
			t.Fatalf("providerName(%v) = %q, want %q", p, got, want)
		}
	}
	if got := providerName(nil); got != "" {
		t.Fatalf("providerName(nil) = %q, want empty", got)
	}
	// The catalog prefix stays a model-id detail and is deliberately not the
	// provider key: deriving one from the other is how pricing lookups silently
	// missed and reported $0.
	if CatalogPrefixOf(Google) == providerName(Google) {
		t.Fatal("google's catalog prefix and provider key must stay distinct")
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
		if got.Provider != providerName(m.Provider) {
			t.Fatalf("row %d provider = %q, want %q", i, got.Provider, providerName(m.Provider))
		}
		if got.Label != m.Label || got.Reasoning != m.Reasoning || got.ContextWindow != m.ContextWindow {
			t.Fatalf("row %d field mismatch: %+v vs %+v", i, got, m)
		}
		if !m.IsAgent() {
			want := m.Provider == Anthropic && m.Mode == ModeAPI
			if got.Configured != want {
				t.Fatalf("row %d (%s) configured = %v, want %v", i, m.ID, got.Configured, want)
			}
		}
	}
}
