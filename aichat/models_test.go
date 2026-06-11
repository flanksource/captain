package aichat

import "testing"

func TestLookupModelDefault(t *testing.T) {
	m, err := LookupModel("")
	if err != nil {
		t.Fatalf("LookupModel(\"\"): %v", err)
	}
	if m.ID != DefaultModelID {
		t.Errorf("default = %q, want %q", m.ID, DefaultModelID)
	}
}

func TestLookupModelUnknownFailsLoud(t *testing.T) {
	if _, err := LookupModel("openai/does-not-exist"); err == nil {
		t.Error("expected error for unknown model, got nil")
	}
}

func TestEffortConfigPerProvider(t *testing.T) {
	cases := []struct {
		name     string
		model    Model
		effort   Effort
		wantKey  string
		wantNil  bool
	}{
		{"openai-high", Model{Provider: ProviderOpenAI, Reasoning: true}, EffortHigh, "reasoning_effort", false},
		{"gemini-medium", Model{Provider: ProviderGoogle, Reasoning: true}, EffortMedium, "thinkingConfig", false},
		{"anthropic-low", Model{Provider: ProviderAnthropic, Reasoning: true}, EffortLow, "thinking", false},
		{"non-reasoning", Model{Provider: ProviderOpenAI, Reasoning: false}, EffortHigh, "", true},
		{"no-effort", Model{Provider: ProviderAnthropic, Reasoning: true}, EffortNone, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := effortConfig(c.model, c.effort)
			if c.wantNil {
				if cfg != nil {
					t.Errorf("cfg = %v, want nil", cfg)
				}
				return
			}
			if _, ok := cfg[c.wantKey]; !ok {
				t.Errorf("cfg %v missing key %q", cfg, c.wantKey)
			}
		})
	}
}

func TestValidateEffort(t *testing.T) {
	for _, e := range []Effort{EffortNone, EffortLow, EffortMedium, EffortHigh} {
		if err := ValidateEffort(e); err != nil {
			t.Errorf("ValidateEffort(%q) = %v, want nil", e, err)
		}
	}
	if err := ValidateEffort(Effort("extreme")); err == nil {
		t.Error("expected error for invalid effort")
	}
}
