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
			"anthropic": {Mode: "agent", Model: "claude-sonnet-5", ReasoningEffort: "high"},
			"openai":    {Mode: "agent", Model: "gpt-5.6-sol", ReasoningEffort: "medium"},
		},
	}
	got, err := applyProviderDefaults(api.Model{Fallbacks: api.ModelList{{Name: "gpt-5.6"}}}, saved)
	if err != nil {
		t.Fatalf("applyProviderDefaults: %v", err)
	}
	if got.Mode != api.ModeAgent || got.Name != "claude-sonnet-5" || got.Effort != api.EffortHigh {
		t.Fatalf("primary = %+v", got)
	}
	if fallback := got.Fallbacks[0]; fallback.Mode != api.ModeAgent || fallback.Effort != api.EffortMedium {
		t.Fatalf("fallback = %+v", fallback)
	}
}

func TestApplyProviderDefaultsPreservesExplicitFields(t *testing.T) {
	saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
		"openai": {Mode: "agent", Model: "gpt-5.6-sol", ReasoningEffort: "medium"},
	}}
	want := api.Model{Name: "gpt-explicit", Mode: api.ModeAPI, Effort: api.EffortHigh}
	got, err := applyProviderDefaults(want, saved)
	if err != nil {
		t.Fatalf("applyProviderDefaults: %v", err)
	}
	if got.Name != want.Name || got.Mode != want.Mode || got.Effort != want.Effort {
		t.Fatalf("explicit model changed: got %+v want %+v", got, want)
	}
	// Provider is not an explicit field — it is derived from the name — so the
	// defaults pass fills it rather than preserving the caller's nil.
	if got.Provider != api.OpenAI {
		t.Fatalf("provider = %v, want the family gpt-explicit names", got.Provider)
	}
}

// A saved mode the provider does not serve is a config error, not something to
// quietly substitute: google has no agent cell, so the default cannot stand.
func TestEffectiveProviderDefaultsRejectsAnUnservedMode(t *testing.T) {
	saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
		"google": {Mode: "agent", Model: "gemini-3.5-flash"},
	}}
	if _, err := effectiveProviderDefaults(saved, api.Google); err == nil {
		t.Fatal("google serves no agent mode; the saved default should fail loud")
	}
}

// With nothing saved, a provider falls to its own declared default — agent
// where it is served, api where it is not. Nothing infers a mode from a model
// name any more, so this is the only place the answer can come from.
func TestEffectiveProviderDefaultsFallsToTheProviderDefault(t *testing.T) {
	for _, p := range api.Providers() {
		view, err := effectiveProviderDefaults(captainconfig.AIDefaults{}, p)
		if err != nil {
			t.Fatalf("effectiveProviderDefaults(%s): %v", p.Name, err)
		}
		if view.Mode != string(p.DefaultMode) {
			t.Errorf("%s default mode = %q, want %q", p.Name, view.Mode, p.DefaultMode)
		}
		if view.Model == "" {
			t.Errorf("%s seeded no model, so a picker would have to hardcode one", p.Name)
		}
	}
}
