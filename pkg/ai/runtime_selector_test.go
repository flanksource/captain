package ai

import (
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestResolveModelSelectors(t *testing.T) {
	cases := []struct {
		name        string
		in          api.Model
		wantName    string
		wantBackend api.Backend
	}{
		{
			name:        "api claude shorthand",
			in:          api.Model{Name: "api:sonnet-5"},
			wantName:    "claude-sonnet-5",
			wantBackend: api.BackendAnthropic,
		},
		{
			name:        "cmux codex shorthand",
			in:          api.Model{Name: "cmux:gpt-5.5"},
			wantName:    "gpt-5.5",
			wantBackend: api.BackendCodexCmux,
		},
		{
			name:        "agent claude shorthand",
			in:          api.Model{Name: "agent:opus"},
			wantName:    "claude-opus-4-8",
			wantBackend: api.BackendClaudeAgent,
		},
		{
			name:        "agent codex app server",
			in:          api.Model{Name: "agent:gpt-5.5"},
			wantName:    "gpt-5.5",
			wantBackend: api.BackendCodexAgent,
		},
		{
			name:        "exact backend",
			in:          api.Model{Name: "claude-cmux:opus"},
			wantName:    "claude-opus-4-8",
			wantBackend: api.BackendClaudeCmux,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveModelSelectors(tc.in)
			if err != nil {
				t.Fatalf("ResolveModelSelectors: %v", err)
			}
			if got.Name != tc.wantName || got.Backend != tc.wantBackend {
				t.Fatalf("got %s/%s, want %s/%s", got.Backend, got.Name, tc.wantBackend, tc.wantName)
			}
		})
	}
}

func TestNormalizeModelForBackend(t *testing.T) {
	cases := []struct {
		backend api.Backend
		model   string
		want    string
	}{
		{api.BackendClaudeAgent, "opus-4-8", "claude-opus-4-8"},
		{api.BackendClaudeCmux, "claude-opus-4-8", "claude-opus-4-8"},
		{api.BackendClaudeCLI, "fable-5", "claude-fable-5"},
		{api.BackendAnthropic, "opus-4-8", "claude-opus-4-8"},
		{api.BackendClaudeAgent, "claude-agent-opus", "claude-opus-4-8"},
		{api.BackendClaudeAgent, "sonnet", "claude-sonnet-5"},
		{api.BackendClaudeAgent, "sonnet-4", "claude-sonnet-4-6"},
		{api.BackendClaudeAgent, "claude-sonnet-4-5", "claude-sonnet-4-6"},
		{api.BackendClaudeAgent, "haiku", "claude-haiku-4-5"},
		{api.BackendClaudeAgent, "haiku-4", "claude-haiku-4-5"},
		{api.BackendClaudeAgent, "haiku-4.5", "claude-haiku-4-5"},
		{api.BackendCodexCmux, "openai/gpt-5.5", "gpt-5.5"},
		{api.BackendCodexAgent, "gpt-5.4-mini", "gpt-5.4-mini"},
	}
	for _, tc := range cases {
		if got := NormalizeModelForBackend(tc.backend, tc.model); got != tc.want {
			t.Fatalf("NormalizeModelForBackend(%s, %q) = %q, want %q", tc.backend, tc.model, got, tc.want)
		}
	}
}

func TestParseModelIdentity(t *testing.T) {
	cases := []struct {
		model string
		want  ModelIdentity
	}{
		{"opus-4-8", ModelIdentity{Provider: modelProviderAnthropic, Family: "opus", Version: "4.8"}},
		{"claude-opus-4-8-3003", ModelIdentity{Provider: modelProviderAnthropic, Family: "opus", Version: "4.8-3003"}},
		{"claude-agent-sonnet", ModelIdentity{Provider: modelProviderAnthropic, Family: "sonnet"}},
		{"openai/gpt-5.5", ModelIdentity{Provider: modelProviderOpenAI, Family: "gpt", Version: "5.5"}},
		{"codex-gpt-5.4", ModelIdentity{Provider: modelProviderOpenAI, Family: "gpt", Version: "5.4"}},
		{"gpt-5.4-mini", ModelIdentity{Provider: modelProviderOpenAI, Family: "gpt", Version: "5.4-mini"}},
	}
	for _, tc := range cases {
		got, ok := ParseModelIdentity(modelProviderAnthropic, tc.model)
		if !ok {
			t.Fatalf("ParseModelIdentity(%q) did not parse", tc.model)
		}
		if got != tc.want {
			t.Fatalf("ParseModelIdentity(%q) = %+v, want %+v", tc.model, got, tc.want)
		}
	}
}

func TestResolveModelSelectors_FallbackSelectors(t *testing.T) {
	got, err := ResolveModelSelectors(api.Model{Name: "api:sonnet-5,cmux:opus"})
	if err != nil {
		t.Fatalf("ResolveModelSelectors: %v", err)
	}
	if got.Name != "claude-sonnet-5" || got.Backend != api.BackendAnthropic {
		t.Fatalf("primary = %s/%s", got.Backend, got.Name)
	}
	if len(got.Fallbacks) != 1 {
		t.Fatalf("fallback count = %d, want 1", len(got.Fallbacks))
	}
	if fb := got.Fallbacks[0]; fb.Name != "claude-opus-4-8" || fb.Backend != api.BackendClaudeCmux {
		t.Fatalf("fallback = %s/%s, want %s/%s", fb.Backend, fb.Name, api.BackendClaudeCmux, "claude-opus-4-8")
	}
}

func TestResolveRuntimeSelectors(t *testing.T) {
	got, err := ResolveRuntimeSelectors(
		[]string{"cli:sonnet-5,cmux:opus", "*:gpt-5.5"},
		api.Model{Name: "claude-sonnet-5", Backend: api.BackendAnthropic},
	)
	if err != nil {
		t.Fatalf("ResolveRuntimeSelectors: %v", err)
	}
	var labels []string
	for _, model := range got {
		labels = append(labels, string(model.Backend)+":"+model.Name)
	}
	want := []string{
		"claude-cli:claude-sonnet-5",
		"claude-cmux:claude-opus-4-8",
		"openai:gpt-5.5",
		"codex-agent:gpt-5.5",
		"codex-cli:gpt-5.5",
		"codex-cmux:gpt-5.5",
	}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels = %#v, want %#v", labels, want)
	}
}

func TestResolveRuntimeSelectors_RequiresModelForPrefixOnly(t *testing.T) {
	got, err := ResolveRuntimeSelectors([]string{"cmux"}, api.Model{Name: "gpt-5.5"})
	if err != nil {
		t.Fatalf("ResolveRuntimeSelectors: %v", err)
	}
	if len(got) != 1 || got[0].Backend != api.BackendCodexCmux || got[0].Name != "gpt-5.5" {
		t.Fatalf("got %#v, want codex-cmux:gpt-5.5", got)
	}
	if _, err := ResolveRuntimeSelectors([]string{"cmux"}, api.Model{}); err == nil {
		t.Fatal("expected missing base model error")
	}
}

func TestResolveRuntimeSelectors_PreservesExplicitUncatalogedVariant(t *testing.T) {
	got, err := ResolveRuntimeSelectors([]string{"agent:gpt-5.4-mini"}, api.Model{})
	if err != nil {
		t.Fatalf("ResolveRuntimeSelectors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d models, want 1: %#v", len(got), got)
	}
	if got[0].Backend != api.BackendCodexAgent || got[0].Name != "gpt-5.4-mini" {
		t.Fatalf("got %s/%s, want %s/gpt-5.4-mini", got[0].Backend, got[0].Name, api.BackendCodexAgent)
	}
}

func TestResolveModelSelectors_WildcardOnlyForMultiModels(t *testing.T) {
	if _, err := ResolveModelSelectors(api.Model{Name: "*:sonnet-5"}); err == nil {
		t.Fatal("expected wildcard selector to be rejected for single model")
	}
}

func TestResolveModelSelectors_UnknownPrefixFails(t *testing.T) {
	if _, err := ResolveModelSelectors(api.Model{Name: "nope:sonnet-5"}); err == nil {
		t.Fatal("expected unknown selector prefix error")
	}
}

func TestResolveModelSelectors_EffortQualifiedAlias(t *testing.T) {
	got, err := ResolveModelSelectors(api.Model{
		Name:   "agent:sol:high",
		Effort: api.EffortLow,
	})
	if err != nil {
		t.Fatalf("ResolveModelSelectors: %v", err)
	}
	if got.Backend != api.BackendCodexAgent || got.Name != "gpt-5.6-sol" || got.Effort != api.EffortHigh {
		t.Fatalf("got %s/%s/%s, want codex-agent/gpt-5.6-sol/high", got.Backend, got.Name, got.Effort)
	}
}

func TestResolveRuntimeSelectors_PerSelectorEffortAndDedup(t *testing.T) {
	got, err := ResolveRuntimeSelectors(
		[]string{"agent:sol:high,agent:sol:xhigh,cmux:terra:max"},
		api.Model{Effort: api.EffortLow},
	)
	if err != nil {
		t.Fatalf("ResolveRuntimeSelectors: %v", err)
	}
	want := []api.Model{
		{Name: "gpt-5.6-sol", Backend: api.BackendCodexAgent, Effort: api.EffortHigh},
		{Name: "gpt-5.6-sol", Backend: api.BackendCodexAgent, Effort: api.EffortXHigh},
		{Name: "gpt-5.6-terra", Backend: api.BackendCodexCmux, Effort: api.EffortMax},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveRuntimeSelectors_WildcardRespectsAvailability(t *testing.T) {
	got, err := ResolveRuntimeSelectors([]string{"*:sol:high"}, api.Model{})
	if err != nil {
		t.Fatalf("ResolveRuntimeSelectors: %v", err)
	}
	var backends []api.Backend
	for _, model := range got {
		backends = append(backends, model.Backend)
		if model.Name != "gpt-5.6-sol" || model.Effort != api.EffortHigh {
			t.Fatalf("unexpected model: %+v", model)
		}
	}
	want := []api.Backend{api.BackendOpenAI, api.BackendCodexAgent, api.BackendCodexCLI, api.BackendCodexCmux}
	if !reflect.DeepEqual(backends, want) {
		t.Fatalf("backends = %v, want %v", backends, want)
	}
}

func TestResolveModelSelectors_EffortErrors(t *testing.T) {
	if _, err := ResolveModelSelectors(api.Model{Name: "agent:sol:extreme"}); err == nil {
		t.Fatal("expected invalid effort suffix error")
	}
}

func TestResolveModelSelectors_OpenAI56VariantsAvailableViaAPI(t *testing.T) {
	for alias, want := range map[string]string{
		"luna":  "gpt-5.6-luna",
		"sol":   "gpt-5.6-sol",
		"terra": "gpt-5.6-terra",
	} {
		t.Run(alias, func(t *testing.T) {
			got, err := ResolveModelSelectors(api.Model{Name: "api:" + alias})
			if err != nil {
				t.Fatalf("ResolveModelSelectors(api:%s): %v", alias, err)
			}
			if got.Backend != api.BackendOpenAI || got.Name != want {
				t.Fatalf("got %s/%s, want openai/%s", got.Backend, got.Name, want)
			}
		})
	}
}
