package registry

import (
	"reflect"
	"strings"
	"testing"
)

// A model name determines the family and nothing else. The composite adapter
// vocabulary used to live on inside model strings — "claude-agent-x" forced the
// agent mode, a bare "codex" forced the CLI — so a name could override a mode the
// caller had explicitly chosen.
func TestProviderForClaimsFamilyOnly(t *testing.T) {
	cases := map[string]*Provider{
		"claude-sonnet-4-6": Anthropic,
		"opus-4-8":          Anthropic,
		"sonnet":            Anthropic,
		"claude-agent-opus": Anthropic,
		"claude-code-x":     Anthropic,
		"gpt-5.5":           OpenAI,
		"o3":                OpenAI,
		"codex":             OpenAI,
		"codex-gpt-5-codex": OpenAI,
		"gemini-2.5-pro":    Google,
		"gemini-cli-pro":    Google,
		"deepseek-chat":     DeepSeek,
	}
	for model, want := range cases {
		got, err := ProviderFor(model)
		if err != nil || got != want {
			t.Errorf("ProviderFor(%q) = %v, %v; want %s", model, got, err, want.Name)
		}
	}
	if _, err := ProviderFor("totally-unknown"); err == nil {
		t.Error("ProviderFor(unknown) should fail loud")
	}
	// grok is no longer claimed by any provider (the codex CLI's grok mode was
	// removed). Pin the removal: a silent claim would resurrect a runtime captain
	// no longer routes.
	if _, err := ProviderFor("grok-2"); err == nil {
		t.Error("ProviderFor(grok-2) should fail loud: grok mode was removed")
	}
}

// The mode a name used to imply must not survive anywhere. These names are the
// exact tokens the deleted composite ids were built from, so if mode inference
// ever comes back this is what fails.
func TestModelNameNeverDecidesMode(t *testing.T) {
	cases := []struct {
		model string
		want  RuntimeMode
	}{
		{model: "claude-agent-opus", want: Anthropic.DefaultMode},
		{model: "claude-code-x", want: Anthropic.DefaultMode},
		{model: "codex", want: OpenAI.DefaultMode},
		{model: "gemini-cli-pro", want: Google.DefaultMode},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got, err := resolveMode("", "", providerOf(t, tc.model))
			if err != nil {
				t.Fatalf("resolveMode: %v", err)
			}
			if got != tc.want {
				t.Errorf("mode for %q = %q, want the provider default %q", tc.model, got, tc.want)
			}
		})
	}
}

// An explicit selection always beats a default, in both spellings.
func TestModeSelectionPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		authored RuntimeMode
		want     RuntimeMode
	}{
		{name: "compact prefix wins over the authored field", prefix: "cli", authored: ModeAPI, want: ModeCLI},
		{name: "authored field wins over the provider default", authored: ModeAPI, want: ModeAPI},
		{name: "provider default when nothing selects", want: Anthropic.DefaultMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMode(tc.prefix, tc.authored, Anthropic)
			if err != nil {
				t.Fatalf("resolveMode: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveMode(%q, %q) = %q, want %q", tc.prefix, tc.authored, got, tc.want)
			}
		})
	}
}

// A local agent run is captain's normal shape. Providers that serve no agent
// mode fall to api rather than to a mode they cannot run.
func TestDefaultModeIsAgentWhereServed(t *testing.T) {
	want := map[*Provider]RuntimeMode{
		Anthropic: ModeAgent,
		OpenAI:    ModeAgent,
		Google:    ModeAPI,
		DeepSeek:  ModeAPI,
	}
	for _, p := range Providers() {
		if p.DefaultMode != want[p] {
			t.Errorf("%s.DefaultMode = %q, want %q", p.Name, p.DefaultMode, want[p])
		}
		if _, ok := p.Caps(p.DefaultMode); !ok {
			t.Errorf("%s defaults to %q, which it does not serve", p.Name, p.DefaultMode)
		}
	}
}

// After Resolve nothing downstream derives a runtime again, so everything a
// driver needs must already be on the model: the exact id it will be sent, the
// mode serving it, and the provider that owns both.
func TestResolvePostcondition(t *testing.T) {
	for _, p := range Providers() {
		for _, mode := range p.Modes() {
			seed, ok := p.latestModel(mode, "")
			if !ok {
				continue
			}
			t.Run(p.Name+"/"+string(mode), func(t *testing.T) {
				got, err := ResolveModel(Model{Name: seed.ID, Mode: mode})
				if err != nil {
					t.Fatalf("ResolveModel(%q, %q): %v", seed.ID, mode, err)
				}
				if got.Provider == nil {
					t.Fatal("resolved model carries no provider")
				}
				if got.Mode == "" {
					t.Fatal("resolved model carries no mode")
				}
				// The name must be a fixed point of the driver-side resolution
				// the adapters used to redo for themselves.
				exact, known := got.Provider.ResolveExact(got.Mode, got.Name)
				if known && exact != got.Name {
					t.Errorf("resolved name %q is not driver-ready: the runtime would send %q", got.Name, exact)
				}
			})
		}
	}
}

// Resolving twice must not move: every boundary resolves, and a model that
// passed through two of them must come out the same as after one.
func TestResolveIsIdempotent(t *testing.T) {
	for _, name := range []string{
		"opus", "agent:opus:high", "anthropic/claude-opus-5", "gemini-2.5-pro",
		// A namespaced id the catalog does not know resolves to a bare name no
		// family claims. Re-resolving it has only the recorded provider to go on,
		// and used to fail where the first pass succeeded.
		"openai/private-model",
	} {
		t.Run(name, func(t *testing.T) {
			once, err := ResolveModel(Model{Name: name})
			if err != nil {
				t.Fatalf("ResolveModel(%q): %v", name, err)
			}
			twice, err := ResolveModel(once)
			if err != nil {
				t.Fatalf("re-resolve: %v", err)
			}
			if !reflect.DeepEqual(once, twice) {
				t.Errorf("resolving twice moved:\n once  = %+v\n twice = %+v", once, twice)
			}
		})
	}
}

// An alias is an input convenience; the driver is handed the model it names.
func TestResolveRendersTheDriverModelID(t *testing.T) {
	got, err := ResolveModel(Model{Name: "luna", Mode: ModeAPI})
	if err != nil {
		t.Fatalf("ResolveModel(luna): %v", err)
	}
	if got.Name == "luna" || !strings.HasPrefix(got.Name, "gpt-") {
		t.Errorf("resolved name = %q, want the concrete gpt id the alias names", got.Name)
	}
}

// WithCapabilities answers about a real provider×mode cell or it errors. The
// silent version returned the model untouched, so every capability read false
// and a caller could not tell that from a genuine answer.
func TestWithCapabilitiesFailsLoud(t *testing.T) {
	if _, err := (Model{Name: "gemini-2.5-pro", Provider: Google, Mode: ModeAgent}).WithCapabilities(); err == nil {
		t.Error("google serves no agent mode; WithCapabilities should fail")
	}
	if _, err := (Model{Name: "not-a-model"}).WithCapabilities(); err == nil {
		t.Error("an unclaimable name should fail rather than return an empty capability set")
	}
	got, err := (Model{Name: "claude-opus-5", Provider: Anthropic, Mode: ModeAgent}).WithCapabilities()
	if err != nil {
		t.Fatalf("WithCapabilities: %v", err)
	}
	if !got.Steer || !got.Interrupt {
		t.Errorf("anthropic agent should steer and interrupt, got %+v", got)
	}
}

// Runtime reads what Resolve recorded. An unresolved model must not quietly
// produce a second answer.
func TestRuntimeRequiresAResolvedModel(t *testing.T) {
	if _, _, err := (Model{Name: "claude-opus-5", Mode: ModeAgent}).Runtime(); err == nil {
		t.Error("a model with no provider should not report a runtime")
	}
	resolved, err := ResolveModel(Model{Name: "claude-opus-5", Mode: ModeAgent})
	if err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	p, mode, err := resolved.Runtime()
	if err != nil || p != Anthropic || mode != ModeAgent {
		t.Errorf("Runtime() = %v, %q, %v", p, mode, err)
	}
}

func TestEffortValidateIncludesCodexTiers(t *testing.T) {
	for _, e := range []Effort{EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortUltra} {
		if err := e.Validate(); err != nil {
			t.Errorf("Effort(%q).Validate() = %v, want nil", e, err)
		}
	}
	if err := Effort("extreme").Validate(); err == nil {
		t.Error("Effort(extreme).Validate() should fail")
	}
	if want := []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortUltra}; !reflect.DeepEqual(AllEfforts(), want) {
		t.Errorf("AllEfforts() = %v, want %v", AllEfforts(), want)
	}
}

func TestRuntimeKindAndAuth(t *testing.T) {
	if ModeAPI.Kind() != "api" || ModeAgent.Kind() != "cli" {
		t.Errorf("Kind() classification wrong: api=%q agent=%q", ModeAPI.Kind(), ModeAgent.Kind())
	}
	if got := AuthEnvVars(Google, ModeAPI); !reflect.DeepEqual(got, []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}) {
		t.Errorf("AuthEnvVars(google, api) = %v", got)
	}
	// A keyless cell rides the local CLI's own login and must consult nothing.
	if got := AuthEnvVars(Anthropic, ModeCmux); len(got) != 0 {
		t.Errorf("AuthEnvVars(anthropic, cmux) = %v, want none", got)
	}
	if !(Runtime{Provider: "openai", Mode: ModeAPI}).Valid() {
		t.Error("openai/api should be a valid runtime")
	}
	if (Runtime{Provider: "google", Mode: ModeAgent}).Valid() {
		t.Error("google serves no agent mode")
	}
}

// Modes come in the wildcard fan-out order, the only ordering users see.
func TestProviderModesOrdering(t *testing.T) {
	if got := OpenAI.Modes(); !reflect.DeepEqual(got, []RuntimeMode{ModeAPI, ModeAgent, ModeCLI, ModeCmux}) {
		t.Fatalf("OpenAI.Modes() = %v", got)
	}
	if got := Google.Modes(); !reflect.DeepEqual(got, []RuntimeMode{ModeAPI, ModeCLI}) {
		t.Fatalf("Google.Modes() = %v", got)
	}
}

func providerOf(t *testing.T, model string) *Provider {
	t.Helper()
	p, err := ProviderFor(model)
	if err != nil {
		t.Fatalf("ProviderFor(%q): %v", model, err)
	}
	return p
}
