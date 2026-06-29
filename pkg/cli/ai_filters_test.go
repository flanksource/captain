package cli

import (
	"sort"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/clicky/aichat"
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
	want := []string{string(aichat.EffortHigh), string(aichat.EffortLow), string(aichat.EffortMedium)}
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

func TestAIFilters_BackendFromAllBackends(t *testing.T) {
	want := make([]string, 0, len(ai.AllBackends()))
	for _, b := range ai.AllBackends() {
		want = append(want, string(b))
	}
	sort.Strings(want)
	if got := optionKeys(t, "backend"); !equalStrings(got, want) {
		t.Errorf("backend options = %v, want %v", got, want)
	}
}

// TestAIFilters_ModelSuggestsBareNames pins that genkit catalog ids are
// suggested without their "provider/" prefix, so the value is directly usable as
// --model (captain's InferBackend matches bare prefixes).
func TestAIFilters_ModelSuggestsBareNames(t *testing.T) {
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
