package registry

import "testing"

// TestEmbeddedRegistryLoads guards the go:embed + JSON parse path: the generated
// models.json must load into a non-empty slice whose every entry is well-formed
// and provider-tagged with a known provider.
func TestEmbeddedRegistryLoads(t *testing.T) {
	if len(knownModels) == 0 {
		t.Fatal("knownModels is empty; models.json failed to load")
	}
	for _, m := range knownModels {
		if m.ID == "" || m.Provider == "" || m.Family == "" {
			t.Errorf("entry %+v is missing id/provider/family", m)
		}
		if _, ok := ProviderByName(m.Provider); !ok {
			t.Errorf("entry %q has unknown provider %q", m.ID, m.Provider)
		}
	}
}

// TestRegistryDerivedCapabilities locks the capability flags the generator
// derives from models.dev: reasoning, temperature support, and the adaptive-vs-
// enabled thinking schema. The opus-4-8/4-7 adaptive values guard the 400 that
// legacy `thinking:{type:enabled}` triggers on those models.
func TestRegistryDerivedCapabilities(t *testing.T) {
	cases := []struct {
		id            string
		wantReasoning bool
		wantTemp      bool
		wantPreferred bool
		wantAdaptive  bool
	}{
		{"claude-opus-5", true, false, true, true},
		{"claude-sonnet-5", true, false, true, true},
		{"claude-fable-5", true, false, true, true},
		{"claude-opus-4-8", true, false, true, true},
		{"claude-opus-4-7", true, false, false, true},
		{"claude-sonnet-4-6", true, true, false, false},
		{"claude-haiku-4-5", true, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			m, ok := Anthropic.Lookup(tc.id)
			if !ok {
				t.Fatalf("model %q not found in registry", tc.id)
			}
			if m.Reasoning != tc.wantReasoning {
				t.Errorf("Reasoning = %v, want %v", m.Reasoning, tc.wantReasoning)
			}
			if m.Temperature != tc.wantTemp {
				t.Errorf("Temperature = %v, want %v", m.Temperature, tc.wantTemp)
			}
			if m.Preferred != tc.wantPreferred {
				t.Errorf("Preferred = %v, want %v", m.Preferred, tc.wantPreferred)
			}
			if m.AdaptiveThinking != tc.wantAdaptive {
				t.Errorf("AdaptiveThinking = %v, want %v", m.AdaptiveThinking, tc.wantAdaptive)
			}
		})
	}
}

// TestRegistryDataCarriesAliasesAndSuccessors pins the knowledge that used to be
// hardcoded in pkg/ai (normalizeCodexVariantAlias, isSupersededRegistryExact) and
// is now catalog data, reachable from every entry point.
func TestRegistryDataCarriesAliasesAndSuccessors(t *testing.T) {
	for alias, want := range map[string]string{"sol": "gpt-5.6-sol", "terra": "gpt-5.6-terra", "luna": "gpt-5.6-luna"} {
		if got := resolveAlias(alias); got != want {
			t.Errorf("resolveAlias(%q) = %q, want %q", alias, got, want)
		}
	}
	for _, retired := range []string{"claude-sonnet-4-5", "claude-sonnet-4-5-20250929"} {
		m, ok := Anthropic.Lookup(retired)
		if !ok {
			t.Fatalf("retired model %q missing from registry", retired)
		}
		if m.SupersededBy != "claude-sonnet-4-6" {
			t.Errorf("%q supersededBy = %q, want claude-sonnet-4-6", retired, m.SupersededBy)
		}
	}
}
