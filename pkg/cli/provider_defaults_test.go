package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

func TestApplyProviderDefaultsFillsMissingFieldsAndFallbacks(t *testing.T) {
	saved := captainconfig.AIDefaults{
		DefaultProvider: "anthropic",
		Providers: map[string]captainconfig.ProviderDefaults{
			"anthropic": {Agent: "claude-agent", Model: "claude-sonnet-5", ReasoningEffort: "high"},
			"openai":    {Agent: "codex-agent", Model: "gpt-5.6-sol", ReasoningEffort: "medium"},
		},
	}
	got, err := applyProviderDefaults(api.Model{Fallbacks: api.ModelList{{Name: "gpt-5.6"}}}, saved)
	if err != nil {
		t.Fatalf("applyProviderDefaults: %v", err)
	}
	if got.Backend != api.BackendClaudeAgent || got.Name != "claude-sonnet-5" || got.Effort != api.EffortHigh {
		t.Fatalf("primary = %+v", got)
	}
	if fallback := got.Fallbacks[0]; fallback.Backend != api.BackendCodexAgent || fallback.Effort != api.EffortMedium {
		t.Fatalf("fallback = %+v", fallback)
	}
}

func TestApplyProviderDefaultsPreservesExplicitFields(t *testing.T) {
	saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
		"openai": {Agent: "codex-agent", Model: "gpt-5.6-sol", ReasoningEffort: "medium"},
	}}
	want := api.Model{Name: "gpt-explicit", Backend: api.BackendOpenAI, Effort: api.EffortHigh}
	got, err := applyProviderDefaults(want, saved)
	if err != nil {
		t.Fatalf("applyProviderDefaults: %v", err)
	}
	if got.Name != want.Name || got.Backend != want.Backend || got.Effort != want.Effort {
		t.Fatalf("explicit model changed: got %+v want %+v", got, want)
	}
}

func TestEffectiveProviderDefaultsRejectsCrossFamilyAgent(t *testing.T) {
	saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
		"anthropic": {Agent: "codex-agent", Model: "claude-sonnet-5"},
	}}
	if _, err := effectiveProviderDefaults(saved, api.BackendAnthropic); err == nil {
		t.Fatal("expected cross-family agent error")
	}
}
