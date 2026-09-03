package aiflags

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// The reported bug in its most reduced form: an unconfigured captain filled the
// mode from Provider.DefaultMode, so a bare model silently became an agent run.
// The registry still does that for parsing and display; a run must not.
func TestResolveForRunRefusesAModeNobodyConfigured(t *testing.T) {
	_, err := ResolveForRunWith(registry.Model{Name: "haiku"}, captainconfig.AIDefaults{})

	if !IsUnconfigured(err) {
		t.Fatalf("want an unconfigured error, got %v", err)
	}
	for _, want := range []string{"no runtime mode configured", "captain configure", "gavel configure"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q:\n%s", want, err)
		}
	}
}

func TestResolveForRunRefusesAModelNobodyConfigured(t *testing.T) {
	_, err := ResolveForRunWith(registry.Model{}, captainconfig.AIDefaults{})

	if !IsUnconfigured(err) {
		t.Fatalf("want an unconfigured error, got %v", err)
	}
	if !strings.Contains(err.Error(), "no model configured") {
		t.Errorf("unexpected message:\n%s", err)
	}
}

// An explicit compact selector is complete on its own and must not need config.
func TestResolveForRunAcceptsAnExplicitSelector(t *testing.T) {
	resolved, err := ResolveForRunWith(registry.Model{Name: "api:haiku"}, captainconfig.AIDefaults{})
	if err != nil {
		t.Fatalf("explicit selector must resolve without config: %v", err)
	}
	if resolved.Mode != registry.ModeAPI {
		t.Errorf("mode = %q, want api", resolved.Mode)
	}
	if resolved.Name != "claude-haiku-4-5" {
		t.Errorf("name = %q, want claude-haiku-4-5", resolved.Name)
	}
}

// The per-provider block is the primary source, and it supplies the mode a bare
// name is missing.
func TestResolveForRunTakesTheProviderBlock(t *testing.T) {
	saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
		registry.Anthropic.Name: {Mode: "api"},
	}}

	resolved, err := ResolveForRunWith(registry.Model{Name: "haiku"}, saved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Mode != registry.ModeAPI {
		t.Errorf("mode = %q, want api from the configured provider block", resolved.Mode)
	}
}

// ai.defaultModel is a compact selector precisely so it can carry a mode; a bare
// name there could not answer the question it exists to answer.
func TestGlobalDefaultModelSuppliesBothHalves(t *testing.T) {
	saved := captainconfig.AIDefaults{DefaultModel: "api:claude-haiku-4-5"}

	resolved, err := ResolveForRunWith(registry.Model{}, saved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Name != "claude-haiku-4-5" || resolved.Mode != registry.ModeAPI {
		t.Errorf("got %s:%s, want api:claude-haiku-4-5", resolved.Mode, resolved.Name)
	}
}

// A selector naming another provider still needs a mechanism, and the global
// default's mode is a mechanism, so it travels even across families.
func TestGlobalDefaultModeAppliesToAnotherProvidersModel(t *testing.T) {
	saved := captainconfig.AIDefaults{DefaultModel: "api:claude-haiku-4-5"}

	resolved, err := ResolveForRunWith(registry.Model{Name: "gemini-3.5-flash"}, saved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Mode != registry.ModeAPI {
		t.Errorf("mode = %q, want api", resolved.Mode)
	}
	if resolved.Name != "gemini-3.5-flash" {
		t.Errorf("the global default must not replace an explicitly named model, got %q", resolved.Name)
	}
}

// EffectiveDefaults still seeds forms from the registry — configure and whoami
// need a value to propose. Only the run path is strict.
func TestEffectiveDefaultsStillSeedsAProposal(t *testing.T) {
	view, err := EffectiveDefaults(captainconfig.AIDefaults{}, registry.Anthropic)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Mode == "" || view.Model == "" {
		t.Errorf("configure/whoami need a proposal, got mode=%q model=%q", view.Mode, view.Model)
	}
	if view.Configured {
		t.Error("an unconfigured provider must still report Configured=false")
	}
}

// SavedDefaults is the run-path view: it reports only what was configured.
func TestSavedDefaultsInventsNothing(t *testing.T) {
	view, err := SavedDefaults(captainconfig.AIDefaults{}, registry.Anthropic)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Model != "" {
		t.Errorf("model = %q, want empty", view.Model)
	}
	if view.Mode != "" {
		t.Errorf("mode = %q, want empty: anthropic serves several modes, so none is implied", view.Mode)
	}
}

// A provider that serves exactly one mode leaves nothing to guess, so naming it
// is arithmetic rather than a default. Without this, configuring one provider
// and then naming a model from a single-mode family would demand a second
// `captain configure` for a mechanism that was never ambiguous.
func TestSingleModeProviderNeedsNoConfiguredMode(t *testing.T) {
	if len(registry.DeepSeek.Modes()) != 1 {
		t.Skipf("deepseek now serves %d modes; this test asserts the single-mode case", len(registry.DeepSeek.Modes()))
	}

	view, err := SavedDefaults(captainconfig.AIDefaults{}, registry.DeepSeek)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Mode == "" {
		t.Error("a provider serving one mode must resolve it without configuration")
	}
}
