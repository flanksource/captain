package ai

import (
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
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
		{"opus-4-8", ModelIdentity{Provider: registry.Anthropic.Name, Family: "opus", Version: "4.8"}},
		{"claude-opus-4-8-3003", ModelIdentity{Provider: registry.Anthropic.Name, Family: "opus", Version: "4.8-3003"}},
		{"claude-agent-sonnet", ModelIdentity{Provider: registry.Anthropic.Name, Family: "sonnet"}},
		{"openai/gpt-5.5", ModelIdentity{Provider: registry.OpenAI.Name, Family: "gpt", Version: "5.5"}},
		{"codex-gpt-5.4", ModelIdentity{Provider: registry.OpenAI.Name, Family: "gpt", Version: "5.4"}},
		{"gpt-5.4-mini", ModelIdentity{Provider: registry.OpenAI.Name, Family: "gpt", Version: "5.4-mini"}},
	}
	for _, tc := range cases {
		got, ok := ParseModelIdentity(registry.Anthropic.Name, tc.model)
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
	// Compare the identity triple: this pins per-selector effort and dedup. The
	// derived capability fields are covered by TestResolvedModelCarriesCapabilities.
	type runtime struct {
		name    string
		backend api.Backend
		effort  api.Effort
	}
	want := []runtime{
		{"gpt-5.6-sol", api.BackendCodexAgent, api.EffortHigh},
		{"gpt-5.6-sol", api.BackendCodexAgent, api.EffortXHigh},
		{"gpt-5.6-terra", api.BackendCodexCmux, api.EffortMax},
	}
	gotRuntimes := make([]runtime, 0, len(got))
	for _, m := range got {
		gotRuntimes = append(gotRuntimes, runtime{m.Name, m.Backend, m.Effort})
	}
	if !reflect.DeepEqual(gotRuntimes, want) {
		t.Fatalf("got %+v, want %+v", gotRuntimes, want)
	}
}

// TestResolvedModelCarriesCapabilities pins that resolution answers what the
// chosen adapter can do, not just which model it is. The values are asserted
// against the known adapters: only the claude agent SDK steers, both agent SDKs
// interrupt, and the claude CLI carries no attachments.
func TestResolvedModelCarriesCapabilities(t *testing.T) {
	cases := []struct {
		selector   string
		wantMode   registry.RuntimeMode
		wantResume bool
		wantIntr   bool
		wantSteer  bool
		wantMedia  []string
	}{
		{"agent:sonnet", registry.ModeAgent, true, true, true, []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}},
		{"cli:sonnet", registry.ModeCLI, true, false, false, []string{}},
		{"api:sonnet", registry.ModeAPI, false, false, false, []string{"image/*"}},
		{"agent:sol", registry.ModeAgent, true, true, false, []string{"image/*"}},
	}
	for _, tc := range cases {
		t.Run(tc.selector, func(t *testing.T) {
			got, err := ResolveModelSelectors(api.Model{Name: tc.selector})
			if err != nil {
				t.Fatalf("ResolveModelSelectors(%q): %v", tc.selector, err)
			}
			if got.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", got.Mode, tc.wantMode)
			}
			if !got.Streaming {
				t.Error("Streaming = false; every adapter streams")
			}
			if got.Resume != tc.wantResume || got.Interrupt != tc.wantIntr || got.Steer != tc.wantSteer {
				t.Errorf("resume/interrupt/steer = %v/%v/%v, want %v/%v/%v",
					got.Resume, got.Interrupt, got.Steer, tc.wantResume, tc.wantIntr, tc.wantSteer)
			}
			if !reflect.DeepEqual(got.MediaTypes, tc.wantMedia) {
				t.Errorf("MediaTypes = %v, want %v", got.MediaTypes, tc.wantMedia)
			}
			if got.Provider == nil {
				t.Fatal("Provider is nil; a resolved model must know its family")
			}
		})
	}
}

// TestModeContradictingBackendFailsLoud: Backend is exactly (provider, mode), so
// a spec naming both must not name two different runtimes.
func TestModeContradictingBackendFailsLoud(t *testing.T) {
	_, err := ResolveModelSelectors(api.Model{Name: "sonnet", Backend: api.BackendAnthropic, Mode: registry.ModeAgent})
	if err == nil {
		t.Fatal("mode agent + backend anthropic should fail loud, not silently pick one")
	}
	if !strings.Contains(err.Error(), "contradicts") {
		t.Errorf("err = %v, want it to name the contradiction", err)
	}
}

// TestModeSelectsBackend: {model, mode} is the object form of the compact
// "mode:model" string and must resolve identically.
func TestModeSelectsBackend(t *testing.T) {
	byMode, err := ResolveModelSelectors(api.Model{Name: "sonnet", Mode: registry.ModeAgent})
	if err != nil {
		t.Fatalf("mode form: %v", err)
	}
	byPrefix, err := ResolveModelSelectors(api.Model{Name: "agent:sonnet"})
	if err != nil {
		t.Fatalf("compact form: %v", err)
	}
	if byMode.Backend != byPrefix.Backend || byMode.Name != byPrefix.Name {
		t.Errorf("mode form = %s/%s, compact form = %s/%s; the two spellings must agree",
			byMode.Backend, byMode.Name, byPrefix.Backend, byPrefix.Name)
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
