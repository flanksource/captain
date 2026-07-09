package ai

import "testing"

// TestEmbeddedRegistryLoads guards the go:embed + JSON parse path: the generated
// model_registry.json must load into a non-empty slice whose every entry is
// well-formed and provider-tagged with a known provider.
func TestEmbeddedRegistryLoads(t *testing.T) {
	if len(exactModelRegistry) == 0 {
		t.Fatal("exactModelRegistry is empty; model_registry.json failed to load")
	}
	knownProviders := map[string]bool{
		modelProviderAnthropic: true,
		modelProviderOpenAI:    true,
		modelProviderGoogle:    true,
		modelProviderDeepSeek:  true,
	}
	for _, m := range exactModelRegistry {
		if m.ID == "" || m.Provider == "" || m.Family == "" {
			t.Errorf("entry %+v is missing id/provider/family", m)
		}
		if !knownProviders[m.Provider] {
			t.Errorf("entry %q has unknown provider %q", m.ID, m.Provider)
		}
	}
}

// TestRegistryKnownModelCorrections locks the patch-driven corrections that
// models.dev reports differently, guarding against the pre-patch staleness that
// motivated this change.
func TestRegistryKnownModelCorrections(t *testing.T) {
	cases := []struct {
		id            string
		wantReasoning bool
		wantPreferred bool
		wantAdaptive  bool
	}{
		{"claude-sonnet-5", false, true, true},
		{"claude-fable-5", true, false, true},
		{"claude-opus-4-8", true, true, false},
		{"claude-haiku-4-5", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			m, ok := lookupRegistryExact(modelProviderAnthropic, tc.id)
			if !ok {
				t.Fatalf("model %q not found in registry", tc.id)
			}
			if m.Reasoning != tc.wantReasoning {
				t.Errorf("Reasoning = %v, want %v", m.Reasoning, tc.wantReasoning)
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

func TestModelUsesAdaptiveThinking(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-5", true},
		{"anthropic/claude-fable-5", true},
		{"sonnet-5", true},
		{"claude-opus-4-8", false},
		{"claude-haiku-4-5", false},
		{"claude-3-5-sonnet-20241022", false},
		{"gpt-5.5", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := ModelUsesAdaptiveThinking(tc.model); got != tc.want {
				t.Errorf("ModelUsesAdaptiveThinking(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}
