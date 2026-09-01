package cli

import (
	"sort"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	capapi "github.com/flanksource/captain/pkg/api"
)

// optionKeys returns the sorted option keys of the aiFilters filter with the
// given key. The completion binder uses these keys as the suggested values.
func optionKeys(t *testing.T, key string) []string {
	t.Helper()
	for _, f := range aiFilters[AIAgentOptions]() {
		if f.Key() != key {
			continue
		}
		opts := f.Options(AIAgentOptions{})
		keys := make([]string, 0, len(opts))
		for k := range opts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	t.Fatalf("no aiFilter with key %q", key)
	return nil
}

func TestAIFilters_EffortFromSharedEnum(t *testing.T) {
	want := make([]string, 0, len(capapi.AllEfforts()))
	for _, e := range capapi.AllEfforts() {
		want = append(want, string(e)) // includes the captain-owned "xhigh" tier
	}
	sort.Strings(want)
	if got := optionKeys(t, "effort"); !equalStrings(got, want) {
		t.Errorf("effort options = %v, want %v", got, want)
	}
}

func TestAIFilters_ScopeFromAllScopes(t *testing.T) {
	want := make([]string, 0, len(agent.AllScopes()))
	for _, s := range agent.AllScopes() {
		want = append(want, string(s))
	}
	sort.Strings(want)
	if got := optionKeys(t, "scope"); !equalStrings(got, want) {
		t.Errorf("scope options = %v, want %v", got, want)
	}
}

// A runtime is two independent axes, so the picker offers two: every mode and
// every provider. The single composite list this replaces could not express
// "cmux off for anthropic but not for openai".
func TestAIFilters_ModeAndProviderFromRegistry(t *testing.T) {
	wantModes := make([]string, 0, len(capapi.AllRuntimeModes()))
	for _, mode := range capapi.AllRuntimeModes() {
		wantModes = append(wantModes, string(mode))
	}
	sort.Strings(wantModes)
	if got := optionKeys(t, "mode"); !equalStrings(got, wantModes) {
		t.Errorf("mode options = %v, want %v", got, wantModes)
	}

	wantProviders := make([]string, 0, len(capapi.Providers()))
	for _, p := range capapi.Providers() {
		wantProviders = append(wantProviders, p.Name)
	}
	sort.Strings(wantProviders)
	if got := optionKeys(t, "provider"); !equalStrings(got, wantProviders) {
		t.Errorf("provider options = %v, want %v", got, wantProviders)
	}
}

// The completion picker is a menu, not a validator: an axis switched off on
// /whoami must not be suggested, while the registry keeps naming it so an
// explicit --mode still fails on the opt-out rather than on "unknown".
func TestAIFilters_ModeAndProviderDropDisabled(t *testing.T) {
	capapi.SetDisabled(capapi.NewDisabledSet([]string{"cmux"}, []string{"deepseek"}, nil, nil, nil))
	t.Cleanup(func() { capapi.SetDisabled(capapi.DisabledSet{}) })

	modes := optionKeys(t, "mode")
	if contains(modes, string(ai.ModeCmux)) {
		t.Errorf("mode options %v still offer disabled cmux", modes)
	}
	if !contains(modes, string(ai.ModeCLI)) {
		t.Errorf("mode options %v dropped the still-enabled cli mode", modes)
	}

	providers := optionKeys(t, "provider")
	if contains(providers, ai.DeepSeek.Name) {
		t.Errorf("provider options %v still offer disabled deepseek", providers)
	}
	if !contains(providers, ai.Anthropic.Name) {
		t.Errorf("provider options %v dropped the still-enabled anthropic", providers)
	}
}

// The --model picker reads ai.Catalog(), so a model the user switched off has
// to stop being suggested — the catalog is filtered when it is read, never when
// it is built at package init.
func TestAIFilters_ModelDropsDisabled(t *testing.T) {
	installTestCatalog(t)
	if !contains(optionKeys(t, "model"), "claude-sonnet-4-5") {
		t.Fatalf("test catalog is missing the model this test disables")
	}

	capapi.SetDisabled(capapi.NewDisabledSet(nil, nil, nil, []string{"claude-sonnet-4-5"}, nil))
	t.Cleanup(func() { capapi.SetDisabled(capapi.DisabledSet{}) })

	if keys := optionKeys(t, "model"); contains(keys, "claude-sonnet-4-5") {
		t.Errorf("model options %v still offer the disabled claude-sonnet-4-5", keys)
	}
}

// TestAIFilters_ModelSuggestsBareNames pins that genkit catalog ids are
// suggested without their "provider/" prefix, so the value is directly usable as
// --model (captain derives the provider from bare prefixes). The synthetic catalog
// keeps the assertion deterministic across clicky/aichat dependency bumps.
func TestAIFilters_ModelSuggestsBareNames(t *testing.T) {
	installTestCatalog(t)

	keys := optionKeys(t, "model")
	if !contains(keys, "claude-sonnet-4-5") {
		t.Errorf("model options %v missing bare name claude-sonnet-4-5", keys)
	}
	for _, k := range keys {
		if k == "anthropic/claude-sonnet-4-5" {
			t.Errorf("model options should be bare names, found provider-prefixed %q", k)
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
