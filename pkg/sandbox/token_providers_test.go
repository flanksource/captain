package sandbox_test

import (
	"testing"

	"github.com/flanksource/captain/pkg/sandbox"
)

// The provider table replaced two hand-written switches that built a
// TokensConfig from selected names. These assert the table reproduces what
// those switches did, and that the two new providers are reachable through it.

func TestApplyTokenSelectionMatchesTheReplacedSwitches(t *testing.T) {
	config := sandbox.ApplyTokenSelection(nil, []string{"aws", "gcp", "azure", "github", "kubernetes"})
	if config == nil {
		t.Fatal("selecting five providers produced no config")
	}
	for name, present := range map[string]bool{
		"aws":        config.AWS != nil,
		"gcp":        config.GCP != nil,
		"azure":      config.Azure != nil,
		"github":     config.GitHub != nil,
		"kubernetes": config.Kubernetes != nil,
	} {
		if !present {
			t.Errorf("provider %q was selected but not set", name)
		}
	}
	if config.Claude != nil || config.Codex != nil {
		t.Error("unselected agent-login providers must stay nil")
	}
}

func TestApplyTokenSelectionReachesTheAgentLoginProviders(t *testing.T) {
	config := sandbox.ApplyTokenSelection(nil, []string{"claude", "codex"})
	if config.Claude == nil || config.Codex == nil {
		t.Fatalf("claude/codex not selectable through the table: %+v", config)
	}
	if config.AWS != nil {
		t.Error("aws must not be set when it was not selected")
	}
}

func TestApplyTokenSelectionDeselectsWithoutDiscardingTheRest(t *testing.T) {
	config := &sandbox.TokensConfig{
		AWS:    &sandbox.AWSTokenConfig{Profile: "prod"},
		Claude: &sandbox.ClaudeTokenConfig{},
	}
	updated := sandbox.ApplyTokenSelection(config, []string{"aws"})

	if updated.Claude != nil {
		t.Error("deselected provider must be cleared")
	}
	if updated.AWS == nil || updated.AWS.Profile != "prod" {
		t.Errorf("a provider that stays selected must keep its settings, got %+v", updated.AWS)
	}
}

func TestApplyTokenSelectionOfNothingProducesNoBlock(t *testing.T) {
	// An untouched wizard must not write `tokens: {}` into a user's config.
	if config := sandbox.ApplyTokenSelection(&sandbox.TokensConfig{}, nil); config != nil {
		t.Errorf("empty selection produced %+v, want nil", config)
	}
}

func TestSelectedTokenProvidersRoundTrips(t *testing.T) {
	want := []string{"gcp", "claude"}
	selected := sandbox.SelectedTokenProviders(sandbox.ApplyTokenSelection(nil, want))

	// SelectedTokenProviders reports in table order, which puts gcp before claude.
	if len(selected) != 2 || selected[0] != "gcp" || selected[1] != "claude" {
		t.Errorf("round trip produced %v, want [gcp claude]", selected)
	}
}

func TestSelectedTokenProvidersOfNilIsEmpty(t *testing.T) {
	if selected := sandbox.SelectedTokenProviders(nil); len(selected) != 0 {
		t.Errorf("nil config reported providers: %v", selected)
	}
}

func TestEveryTokenProviderIsWiredIntoTheTable(t *testing.T) {
	// A provider whose Set does not actually set anything would be selectable in
	// the wizard and then never acquired — the failure the table exists to stop.
	for _, provider := range sandbox.TokenProviders() {
		config := &sandbox.TokensConfig{}
		provider.Set(config)
		if !provider.Enabled(config) {
			t.Errorf("provider %q: Set did not make Enabled true", provider.Name)
		}
		provider.Clear(config)
		if provider.Enabled(config) {
			t.Errorf("provider %q: Clear did not make Enabled false", provider.Name)
		}
		if provider.Label == "" {
			t.Errorf("provider %q has no label for the selection widget", provider.Name)
		}
	}
}
