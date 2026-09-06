package ai

import (
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

func TestResolveCompactSelectors(t *testing.T) {
	cases := []struct {
		name     string
		in       api.Model
		wantName string
		wantMode api.RuntimeMode
	}{
		{
			name:     "api claude shorthand",
			in:       api.Model{Name: "api:sonnet-5"},
			wantName: "claude-sonnet-5",
			wantMode: api.ModeAPI,
		},
		{
			name:     "cmux codex shorthand",
			in:       api.Model{Name: "cmux:gpt-5.5"},
			wantName: "gpt-5.5",
			wantMode: api.ModeCmux,
		},
		{
			name:     "agent claude shorthand",
			in:       api.Model{Name: "agent:opus"},
			wantName: "claude-opus-5",
			wantMode: api.ModeAgent,
		},
		{
			name:     "agent codex app server",
			in:       api.Model{Name: "agent:gpt-5.5"},
			wantName: "gpt-5.5",
			wantMode: api.ModeAgent,
		},
		{
			// The compact prefix is a runtime mode; the family comes from the
			// model name, so the two together name exactly one adapter.
			name:     "exact mode",
			in:       api.Model{Name: "cmux:opus"},
			wantName: "claude-opus-5",
			wantMode: api.ModeCmux,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.in)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Name != tc.wantName || got.Mode != tc.wantMode {
				t.Fatalf("got %s:%s, want %s:%s", got.Mode, got.Name, tc.wantMode, tc.wantName)
			}
		})
	}
}

// Resolve hands the driver the id it accepts. Aliases, family names and provider
// namespaces are input conveniences; none of them survive. The adapters used to
// redo this themselves — nine of them, each discarding the failure — so the id
// captain recorded and the id the driver received could differ.
func TestResolveRendersTheDriverModelID(t *testing.T) {
	cases := []struct {
		selector string
		want     string
	}{
		{"agent:opus-4-8", "claude-opus-4-8"},
		{"cmux:claude-opus-4-8", "claude-opus-4-8"},
		{"cli:fable-5", "claude-fable-5-1"},
		{"api:opus-4-8", "claude-opus-4-8"},
		{"agent:sonnet", "claude-sonnet-5"},
		{"agent:sonnet-4", "claude-sonnet-4-6"},
		{"agent:claude-sonnet-4-5", "claude-sonnet-4-6"},
		{"agent:haiku", "claude-haiku-4-5"},
		{"agent:haiku-4", "claude-haiku-4-5"},
		{"agent:haiku-4.5", "claude-haiku-4-5"},
		{"cmux:openai/gpt-5.5", "gpt-5.5"},
		// An id the catalog does not know is still a real model the provider
		// offers, so it passes through rather than being rejected.
		{"agent:gpt-5.4-mini", "gpt-5.4-mini"},
	}
	for _, tc := range cases {
		got, err := Resolve(api.Model{Name: tc.selector})
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tc.selector, err)
		}
		if got.Name != tc.want {
			t.Fatalf("Resolve(%q) = %q, want %q", tc.selector, got.Name, tc.want)
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
	// "claude-agent-sonnet" was a composite adapter id, not a model. Nothing
	// mints it any more, and the trim table no longer privileges it — so it
	// parses as whatever its literal text says, never as sonnet-on-agent.
	if got, _ := ParseModelIdentity(registry.Anthropic.Name, "claude-agent-sonnet"); got.Family == "sonnet" {
		t.Errorf("the removed composite id still resolves to a family: %+v", got)
	}
}

func TestResolve_FallbackSelectors(t *testing.T) {
	got, err := Resolve(api.Model{Name: "api:sonnet-5,cmux:opus"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "claude-sonnet-5" || got.Provider != api.Anthropic || got.Mode != api.ModeAPI {
		t.Fatalf("primary = %s %s/%s", got.Provider.Name, got.Mode, got.Name)
	}
	if len(got.Fallbacks) != 1 {
		t.Fatalf("fallback count = %d, want 1", len(got.Fallbacks))
	}
	if fb := got.Fallbacks[0]; fb.Name != "claude-opus-5" || fb.Provider != api.Anthropic || fb.Mode != api.ModeCmux {
		t.Fatalf("fallback = %s %s/%s, want anthropic cmux/claude-opus-5", fb.Provider.Name, fb.Mode, fb.Name)
	}
}

func TestResolveMulti_ExpandsEachSelector(t *testing.T) {
	got, err := ResolveMulti(
		[]string{"cli:sonnet-5,cmux:opus", "*:gpt-5.5"},
		api.Model{Name: "claude-sonnet-5", Mode: api.ModeAPI},
	)
	if err != nil {
		t.Fatalf("ResolveMulti: %v", err)
	}
	var labels []string
	for _, model := range got {
		labels = append(labels, model.Provider.Name+" "+string(model.Mode)+":"+model.Name)
	}
	want := []string{
		"anthropic cli:claude-sonnet-5",
		"anthropic cmux:claude-opus-5",
		"openai api:gpt-5.5",
		"openai agent:gpt-5.5",
		"openai cli:gpt-5.5",
		"openai cmux:gpt-5.5",
	}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels = %#v, want %#v", labels, want)
	}
}

func TestResolveMulti_RequiresModelForPrefixOnly(t *testing.T) {
	got, err := ResolveMulti([]string{"cmux"}, api.Model{Name: "gpt-5.5"})
	if err != nil {
		t.Fatalf("ResolveMulti: %v", err)
	}
	if len(got) != 1 || got[0].Provider != api.OpenAI || got[0].Mode != api.ModeCmux || got[0].Name != "gpt-5.5" {
		t.Fatalf("got %#v, want codex-cmux:gpt-5.5", got)
	}
	if _, err := ResolveMulti([]string{"cmux"}, api.Model{}); err == nil {
		t.Fatal("expected missing base model error")
	}
}

func TestResolveMulti_PreservesExplicitUncatalogedVariant(t *testing.T) {
	got, err := ResolveMulti([]string{"agent:gpt-5.4-mini"}, api.Model{})
	if err != nil {
		t.Fatalf("ResolveMulti: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d models, want 1: %#v", len(got), got)
	}
	if got[0].Provider != api.OpenAI || got[0].Mode != api.ModeAgent || got[0].Name != "gpt-5.4-mini" {
		t.Fatalf("got %s:%s, want agent:gpt-5.4-mini", got[0].Mode, got[0].Name)
	}
}

func TestResolve_WildcardOnlyForMultiModels(t *testing.T) {
	if _, err := Resolve(api.Model{Name: "*:sonnet-5"}); err == nil {
		t.Fatal("expected wildcard selector to be rejected for single model")
	}
}

func TestResolve_UnknownPrefixFails(t *testing.T) {
	if _, err := Resolve(api.Model{Name: "nope:sonnet-5"}); err == nil {
		t.Fatal("expected unknown selector prefix error")
	}
}

func TestResolve_EffortQualifiedAlias(t *testing.T) {
	got, err := Resolve(api.Model{
		Name:   "agent:sol:high",
		Effort: api.EffortLow,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Provider != api.OpenAI || got.Mode != api.ModeAgent || got.Name != "gpt-5.6-sol" || got.Effort != api.EffortHigh {
		t.Fatalf("got %s:%s:%s, want agent:gpt-5.6-sol:high", got.Mode, got.Name, got.Effort)
	}
}

func TestResolveMulti_PerSelectorEffortAndDedup(t *testing.T) {
	got, err := ResolveMulti(
		[]string{"agent:sol:high,agent:sol:xhigh,cmux:terra:max"},
		api.Model{Effort: api.EffortLow},
	)
	if err != nil {
		t.Fatalf("ResolveMulti: %v", err)
	}
	// Compare the identity triple: this pins per-selector effort and dedup. The
	// derived capability fields are covered by TestResolvedModelCarriesCapabilities.
	type runtime struct {
		name   string
		mode   api.RuntimeMode
		effort api.Effort
	}
	want := []runtime{
		{"gpt-5.6-sol", api.ModeAgent, api.EffortHigh},
		{"gpt-5.6-sol", api.ModeAgent, api.EffortXHigh},
		{"gpt-5.6-terra", api.ModeCmux, api.EffortMax},
	}
	gotRuntimes := make([]runtime, 0, len(got))
	for _, m := range got {
		gotRuntimes = append(gotRuntimes, runtime{m.Name, m.Mode, m.Effort})
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
		wantTools  bool
		wantMedia  []string
	}{
		{"agent:sonnet", registry.ModeAgent, true, true, true, true, []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}},
		{"cli:sonnet", registry.ModeCLI, true, false, false, false, []string{}},
		{"api:sonnet", registry.ModeAPI, false, false, false, true, []string{"image/*"}},
		{"agent:sol", registry.ModeAgent, true, true, false, true, []string{"image/*"}},
	}
	for _, tc := range cases {
		t.Run(tc.selector, func(t *testing.T) {
			got, err := Resolve(api.Model{Name: tc.selector})
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.selector, err)
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
			if got.CallerTools != tc.wantTools {
				t.Errorf("CallerTools = %v, want %v", got.CallerTools, tc.wantTools)
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

// A model can no longer name two runtimes: there is one mode field and the
// provider is derived from the name, so the contradiction the deleted Backend
// field made possible is now unrepresentable. What remains is precedence — a
// compact prefix beats the mode field — which the registry suite pins.
func TestCompactPrefixBeatsTheModeField(t *testing.T) {
	got, err := Resolve(api.Model{Name: "agent:sonnet", Mode: api.ModeAPI})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Mode != api.ModeAgent {
		t.Fatalf("mode = %q, want the compact prefix to win", got.Mode)
	}
}

// TestModeFieldMatchesCompactPrefix: {model, mode} is the object form of the
// compact "mode:model" string and must resolve identically.
func TestModeFieldMatchesCompactPrefix(t *testing.T) {
	byMode, err := Resolve(api.Model{Name: "sonnet", Mode: registry.ModeAgent})
	if err != nil {
		t.Fatalf("mode form: %v", err)
	}
	byPrefix, err := Resolve(api.Model{Name: "agent:sonnet"})
	if err != nil {
		t.Fatalf("compact form: %v", err)
	}
	if byMode.Provider != byPrefix.Provider || byMode.Mode != byPrefix.Mode || byMode.Name != byPrefix.Name {
		t.Errorf("mode form = %s %s/%s, compact form = %s %s/%s; the two spellings must agree",
			byMode.Provider.Name, byMode.Mode, byMode.Name,
			byPrefix.Provider.Name, byPrefix.Mode, byPrefix.Name)
	}
}

// A wildcard fans out over the modes its provider actually serves, in the
// registry's declared order.
func TestResolveMulti_WildcardRespectsAvailability(t *testing.T) {
	got, err := ResolveMulti([]string{"*:sol:high"}, api.Model{})
	if err != nil {
		t.Fatalf("ResolveMulti: %v", err)
	}
	var modes []api.RuntimeMode
	for _, model := range got {
		if model.Provider != api.OpenAI {
			t.Fatalf("provider = %v, want openai", model.Provider)
		}
		modes = append(modes, model.Mode)
		if model.Name != "gpt-5.6-sol" || model.Effort != api.EffortHigh {
			t.Fatalf("unexpected model: %+v", model)
		}
	}
	want := []api.RuntimeMode{api.ModeAPI, api.ModeAgent, api.ModeCLI, api.ModeCmux}
	if !reflect.DeepEqual(modes, want) {
		t.Fatalf("modes = %v, want %v", modes, want)
	}
}

func TestResolve_EffortErrors(t *testing.T) {
	if _, err := Resolve(api.Model{Name: "agent:sol:extreme"}); err == nil {
		t.Fatal("expected invalid effort suffix error")
	}
}

func TestResolve_OpenAI56VariantsAvailableViaAPI(t *testing.T) {
	for alias, want := range map[string]string{
		"luna":  "gpt-5.6-luna",
		"sol":   "gpt-5.6-sol",
		"terra": "gpt-5.6-terra",
	} {
		t.Run(alias, func(t *testing.T) {
			got, err := Resolve(api.Model{Name: "api:" + alias})
			if err != nil {
				t.Fatalf("Resolve(api:%s): %v", alias, err)
			}
			if got.Provider != api.OpenAI || got.Mode != api.ModeAPI || got.Name != want {
				t.Fatalf("got %s %s:%s, want openai api:%s", got.Provider.Name, got.Mode, got.Name, want)
			}
		})
	}
}
