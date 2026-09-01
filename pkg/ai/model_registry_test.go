package ai

import "testing"

func TestRegistryModelDefsIncludeFableCapabilities(t *testing.T) {
	for _, mode := range []RuntimeMode{ModeAgent, ModeCLI, ModeCmux} {
		defs := RegistryModelDefs(Anthropic, mode)
		var fable *ModelDef
		for i := range defs {
			if defs[i].ID == "claude-fable-5" {
				fable = &defs[i]
				break
			}
		}
		if fable == nil {
			t.Fatalf("anthropic %s registry models omit claude-fable-5: %+v", mode, defs)
		}
		if !fable.CapabilitiesKnown || !fable.Reasoning || fable.Temperature {
			t.Fatalf("anthropic %s fable capabilities = %+v", mode, *fable)
		}
		if len(fable.SupportedEfforts) != 5 || fable.SupportedEfforts[0] != "low" || fable.SupportedEfforts[4] != "max" {
			t.Fatalf("anthropic %s fable efforts = %v", mode, fable.SupportedEfforts)
		}
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
		{"claude-opus-4-8", true},
		{"claude-opus-4-7", true},
		{"claude-haiku-4-5", false},
		{"claude-sonnet-4-6", false},
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
