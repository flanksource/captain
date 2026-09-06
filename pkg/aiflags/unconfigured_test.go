package aiflags

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

func TestDefaultsReportAModeNobodyConfigured(t *testing.T) {
	result, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "haiku"}})
	if err != nil || len(result.Unconfigured) != 1 {
		t.Fatalf("want one unconfigured candidate, got %v: %v", result.Unconfigured, err)
	}
	err = result.Unconfigured[0]

	if !IsUnconfigured(err) {
		t.Fatalf("want an unconfigured error, got %v", err)
	}
	for _, want := range []string{"no runtime mode configured", "captain configure", "gavel configure"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q:\n%s", want, err)
		}
	}
}

func TestUnconfiguredModelNamesTheRemediation(t *testing.T) {
	err := &UnconfiguredError{Field: "model"}

	if !IsUnconfigured(err) {
		t.Fatalf("want an unconfigured error, got %v", err)
	}
	if !strings.Contains(err.Error(), "no model configured") {
		t.Errorf("unexpected message:\n%s", err)
	}
}

// An explicit compact selector is complete on its own and must not need config.
func TestDefaultsAcceptAnExplicitSelector(t *testing.T) {
	result, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "api:haiku"}})
	resolved := result.Model
	if err != nil {
		t.Fatalf("explicit selector must resolve without config: %v", err)
	}
	if resolved.Mode != registry.ModeAPI {
		t.Errorf("mode = %q, want api", resolved.Mode)
	}
	if resolved.Name != "haiku" {
		t.Errorf("name = %q, want unresolved alias haiku", resolved.Name)
	}
}

// The per-provider block is the primary source, and it supplies the mode a bare
// name is missing.
func TestDefaultsTakeTheProviderBlock(t *testing.T) {
	saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
		registry.Anthropic.Name: {Mode: "api"},
	}}

	result, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "haiku"}, Saved: saved})
	resolved := result.Model
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

	result, err := ApplyDefaults(DefaultOptions{Saved: saved})
	resolved := result.Model
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Name != "claude-haiku-4-5" || resolved.Mode != registry.ModeAPI {
		t.Errorf("got %s:%s, want api:claude-haiku-4-5", resolved.Mode, resolved.Name)
	}
}

func TestGlobalDefaultModeDoesNotLeakToAnotherProvider(t *testing.T) {
	saved := captainconfig.AIDefaults{DefaultModel: "api:claude-haiku-4-5"}

	result, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "gemini-3.5-flash"}, Saved: saved})
	resolved := result.Model
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Mode != "" || len(result.Unconfigured) != 1 {
		t.Errorf("mode = %q, want one reported unconfigured mode: %v", resolved.Mode, result.Unconfigured)
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
