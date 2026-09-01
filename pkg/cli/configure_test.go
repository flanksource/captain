package cli

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/credentials"
	clickyrpc "github.com/flanksource/clicky/rpc"
	"github.com/flanksource/clicky/text"
)

func TestRunConfigureRejectsGeneratedRPCInvocation(t *testing.T) {
	ctx := clickyrpc.ContextWithRequest(context.Background(), httptest.NewRequest("POST", "/api/v1/configure", nil))
	if _, err := RunConfigure(ctx, ConfigureOptions{Provider: "openai", Test: true}); err == nil {
		t.Fatal("generated configure RPC must direct callers to the guarded provider-token endpoints")
	}
}

func TestRunProviderConfigureTestsBeforeSaving(t *testing.T) {
	credentials.SetPathForTesting(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { credentials.SetPathForTesting("") })
	previous := configureTokenModels
	configureTokenModels = func(_ context.Context, provider *ai.ModelProvider, token string) ([]ai.ModelDef, error) {
		if provider != ai.OpenAI || token != "candidate-secret" {
			t.Fatalf("validation got provider=%s token=%q", provider.Name, token)
		}
		return []ai.ModelDef{{ID: "gpt-example"}}, nil
	}
	t.Cleanup(func() { configureTokenModels = previous })

	result, err := runProviderTokenConfigure(context.Background(), ai.OpenAI, ConfigureOptions{
		Token: text.SensitiveString("candidate-secret"),
	})
	if err != nil {
		t.Fatalf("runProviderConfigure: %v", err)
	}
	if result.Provider != "openai" || result.ModelCount != 1 || !result.Saved || result.MaskedToken != "cand…cret" {
		t.Fatalf("result = %+v", result)
	}
	vault, _ := credentials.DefaultVault()
	values, err := vault.Load()
	if err != nil || values["openai"] != "candidate-secret" {
		t.Fatalf("vault = %v, err=%v", values, err)
	}
}

func TestRunProviderConfigureFailedValidationPreservesToken(t *testing.T) {
	credentials.SetPathForTesting(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { credentials.SetPathForTesting("") })
	vault, _ := credentials.DefaultVault()
	if err := vault.Set("anthropic", "existing-secret"); err != nil {
		t.Fatalf("seed vault: %v", err)
	}
	previous := configureTokenModels
	configureTokenModels = func(context.Context, *ai.ModelProvider, string) ([]ai.ModelDef, error) {
		return nil, errors.New("credential rejected")
	}
	t.Cleanup(func() { configureTokenModels = previous })

	_, err := runProviderTokenConfigure(context.Background(), ai.Anthropic, ConfigureOptions{
		Token: text.SensitiveString("invalid-secret"),
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	values, loadErr := vault.Load()
	if loadErr != nil || values["anthropic"] != "existing-secret" {
		t.Fatalf("vault = %v, err=%v", values, loadErr)
	}
}

func TestRunProviderConfigureTestCurrentDoesNotWrite(t *testing.T) {
	credentials.SetPathForTesting(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { credentials.SetPathForTesting("") })
	t.Setenv("DEEPSEEK_API_KEY", "environment-secret")
	previous := configureTokenModels
	configureTokenModels = func(_ context.Context, provider *ai.ModelProvider, token string) ([]ai.ModelDef, error) {
		if provider != ai.DeepSeek || token != "environment-secret" {
			t.Fatalf("validation got provider=%s token=%q", provider.Name, token)
		}
		return []ai.ModelDef{{ID: "deepseek-example"}}, nil
	}
	t.Cleanup(func() { configureTokenModels = previous })

	result, err := runProviderTokenConfigure(context.Background(), ai.DeepSeek, ConfigureOptions{Test: true})
	if err != nil {
		t.Fatalf("runProviderConfigure: %v", err)
	}
	if result.Saved || result.Source != credentials.SourceEnvironment {
		t.Fatalf("result = %+v", result)
	}
	vault, _ := credentials.DefaultVault()
	values, err := vault.Load()
	if err != nil || len(values) != 0 {
		t.Fatalf("test-only run wrote vault: %v, err=%v", values, err)
	}
}

func TestRunProviderDefaultsConfigureSavesPartialDefaultsAndActiveProvider(t *testing.T) {
	captainconfig.SetPathForTesting(filepath.Join(t.TempDir(), ".captain.yaml"))
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
	if err := captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{
		DefaultProvider: "anthropic",
		Providers: map[string]captainconfig.ProviderDefaults{
			"anthropic": {Mode: "agent", Model: "claude-existing", ReasoningEffort: "low"},
			"openai":    {Mode: "api", Model: "gpt-existing", ReasoningEffort: "medium"},
		},
	}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	previous := configureDefaultsModels
	configureDefaultsModels = func(_ context.Context, provider *api.ModelProvider, mode api.RuntimeMode) ([]ai.ModelDef, error) {
		if provider != api.OpenAI || mode != api.ModeAgent {
			t.Fatalf("runtime = %s %s", provider.Name, mode)
		}
		return []ai.ModelDef{{ID: "gpt-5.6-sol"}}, nil
	}
	t.Cleanup(func() { configureDefaultsModels = previous })

	result, err := runProviderDefaultsConfigure(context.Background(), api.OpenAI, ConfigureOptions{
		Mode: "agent", Active: true,
	})
	if err != nil {
		t.Fatalf("runProviderDefaultsConfigure: %v", err)
	}
	if result.Mode != "agent" || result.Model != "gpt-5.6-sol" || !result.Active {
		t.Fatalf("result = %+v", result)
	}
	got, _, err := captainconfig.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got.AI.DefaultProvider != "openai" || got.AI.Providers["openai"].Mode != "agent" || got.AI.Providers["anthropic"].Model != "claude-existing" {
		t.Fatalf("saved config = %+v", got.AI)
	}
}

func TestRunProviderDefaultsConfigureDefaultEffortClearsSavedEffort(t *testing.T) {
	captainconfig.SetPathForTesting(filepath.Join(t.TempDir(), ".captain.yaml"))
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
	if err := captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{
		Providers: map[string]captainconfig.ProviderDefaults{
			"openai": {Mode: "agent", Model: "gpt-5.6-sol", ReasoningEffort: "high"},
		},
	}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	previous := configureDefaultsModels
	configureDefaultsModels = func(context.Context, *api.ModelProvider, api.RuntimeMode) ([]ai.ModelDef, error) {
		return []ai.ModelDef{{ID: "gpt-5.6-sol"}}, nil
	}
	t.Cleanup(func() { configureDefaultsModels = previous })

	result, err := runProviderDefaultsConfigure(context.Background(), api.OpenAI, ConfigureOptions{Effort: "default"})
	if err != nil {
		t.Fatalf("runProviderDefaultsConfigure: %v", err)
	}
	got, _, loadErr := captainconfig.Load()
	if loadErr != nil || result.Effort != "" || got.AI.Providers["openai"].ReasoningEffort != "" {
		t.Fatalf("result=%+v config=%+v err=%v", result, got.AI, loadErr)
	}
}

func TestRunProviderConfigureRejectsCredentialAndDefaultFlagsTogether(t *testing.T) {
	_, err := runProviderConfigure(context.Background(), ConfigureOptions{
		Provider: "openai", Model: "gpt-5.6", Token: text.SensitiveString("candidate-secret"),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildConfigFromForm_TogglesInvert(t *testing.T) {
	in := formInputs{
		Provider:        api.Anthropic,
		Mode:            "agent",
		Model:           "claude-sonnet-4-6",
		ReasoningEffort: "medium",
		BudgetUSD:       "1.50",
		MaxTokens:       "8192",
		Timeout:         " 180s ",
		Enabled:         []string{toggleCaching, toggleMCP, toggleHooks},
	}
	got := buildConfigFromForm(in)
	want := captainconfig.Config{
		AI: captainconfig.AIDefaults{
			DefaultProvider: "anthropic",
			Providers: map[string]captainconfig.ProviderDefaults{
				"anthropic": {Mode: "agent", Model: "claude-sonnet-4-6", ReasoningEffort: "medium"},
			},
			BudgetUSD: 1.5,
			MaxTokens: 8192,
			Timeout:   "180s",
			NoCache:   false,
			NoMCP:     false,
			NoHooks:   false,
			NoSkills:  true,
			NoUser:    true,
			NoProject: true,
			NoMemory:  true,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildConfigFromForm()\n got  = %+v\n want = %+v", got, want)
	}
}

func TestBuildConfigFromForm_BlankNumericInputs(t *testing.T) {
	got := buildConfigFromForm(formInputs{
		Provider: api.OpenAI,
		Mode:     "api",
		Model:    "gpt-5",
		Enabled:  allToggles, // all enabled
	})
	if got.AI.BudgetUSD != 0 {
		t.Errorf("blank budget should parse to 0, got %v", got.AI.BudgetUSD)
	}
	if got.AI.MaxTokens != 0 {
		t.Errorf("blank max tokens should parse to 0, got %v", got.AI.MaxTokens)
	}
	if got.AI.NoCache || got.AI.NoMCP || got.AI.NoHooks || got.AI.NoSkills ||
		got.AI.NoUser || got.AI.NoProject || got.AI.NoMemory {
		t.Errorf("all toggles enabled should leave every No* flag false, got %+v", got.AI)
	}
}

func TestTogglesFromConfig_RoundTripsThroughBuildConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		ai   captainconfig.AIDefaults
		want []string
	}{
		{
			name: "all disabled",
			ai: captainconfig.AIDefaults{
				NoCache: true, NoMCP: true, NoHooks: true, NoSkills: true,
				NoUser: true, NoProject: true, NoMemory: true,
			},
			want: nil,
		},
		{
			name: "all enabled",
			ai:   captainconfig.AIDefaults{},
			want: append([]string(nil), allToggles...),
		},
		{
			name: "mixed",
			ai:   captainconfig.AIDefaults{NoMCP: true, NoMemory: true},
			want: []string{toggleCaching, toggleHooks, toggleSkills, toggleUser, toggleProject},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := togglesFromConfig(tc.ai)
			sortedGot := append([]string(nil), got...)
			sortedWant := append([]string(nil), tc.want...)
			sort.Strings(sortedGot)
			sort.Strings(sortedWant)
			if !reflect.DeepEqual(sortedGot, sortedWant) {
				t.Errorf("togglesFromConfig() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestModelOptionsFor_NoKeyShowsErrorRow(t *testing.T) {
	// API backends have no static fallback: a missing key surfaces as a single
	// sentinel option carrying the error so the user can fix their environment
	// without leaving the wizard. CLI/agent backends are covered separately —
	// they list from the catalog and never require a key.
	credentials.SetPathForTesting(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { credentials.SetPathForTesting("") })
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")

	for _, p := range ai.Providers() {
		opts := modelOptionsFor(p, ai.ModeAPI)
		if len(opts) != 1 {
			t.Errorf("%s api: expected 1 sentinel row, got %d (%+v)", p.Name, len(opts), opts)
			continue
		}
		if !strings.Contains(opts[0].Key, "no models") {
			t.Errorf("%s api: sentinel row should carry an error message, got %+v", p.Name, opts[0])
		}
	}
}

// TestModelOptionsFor_LocalModesUseCatalogWithoutKey verifies the cli and agent
// modes populate the picker from the static catalog with no API key set: they
// authenticate internally, so the wizard must never gate them on a key.
func TestModelOptionsFor_LocalModesUseCatalogWithoutKey(t *testing.T) {
	installTestCatalog(t)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	opts := modelOptionsFor(ai.OpenAI, ai.ModeCLI)
	if len(opts) != 1 {
		t.Fatalf("codex-cli picker = %+v, want a single catalog option", opts)
	}
	if opts[0].Value != "gpt-5.5" {
		t.Errorf("codex-cli option value = %q, want exact model gpt-5.5", opts[0].Value)
	}
}

// TestDefaultModelFor_SeedsARunnableModel pins the derivation rather than a
// table of ids: the seed is captain's declared default wherever the backend can
// run it, and the backend provider's own current pick otherwise. The literal
// table this replaced had gone stale against the catalog it seeded from.
func TestDefaultModelFor_SeedsARunnableModel(t *testing.T) {
	declared, err := ai.LookupModel(ai.DefaultModelID)
	if err != nil {
		t.Fatalf("catalog does not carry the declared default: %v", err)
	}
	for _, mode := range ai.Anthropic.Modes() {
		if got := defaultModelFor(ai.Anthropic, mode); got != declared.BareID() {
			t.Errorf("defaultModelFor(anthropic %s) = %q, want the declared default %q", mode, got, declared.BareID())
		}
	}

	// Every other runtime seeds a model its own provider offers on that mode —
	// never a claude id, and never one the catalog cannot run there.
	for _, p := range ai.Providers() {
		for _, mode := range p.Modes() {
			seed := defaultModelFor(p, mode)
			if seed == "" {
				t.Errorf("defaultModelFor(%s %s) seeded nothing", p.Name, mode)
				continue
			}
			if known, available := ai.RegistryModelAvailability(p, mode, seed); !known || !available {
				t.Errorf("defaultModelFor(%s %s) = %q, which that runtime cannot run", p.Name, mode, seed)
			}
		}
	}
}

// TestEffectiveProviderDefaults_SkipsDisabledModels covers the seed a picker
// would otherwise pre-select after the user switched that exact model off.
//
// The opt-out is applied here rather than in defaultModelFor: that table is
// deliberately hand-maintained (deriving it would move every unconfigured user's
// default whenever the catalog snapshot changes), so degrading a switched-off
// selection is this projection's job.
func TestEffectiveProviderDefaults_SkipsDisabledModels(t *testing.T) {
	seed := defaultModelFor(ai.Anthropic, ai.Anthropic.DefaultMode)
	api.SetDisabled(api.NewDisabledSet(nil, nil, nil, []string{seed}, nil))
	t.Cleanup(func() { api.SetDisabled(api.DisabledSet{}) })

	view, err := effectiveProviderDefaults(captainconfig.AIDefaults{}, ai.Anthropic)
	if err != nil {
		t.Fatalf("effectiveProviderDefaults: %v", err)
	}
	if view.Model == seed {
		t.Fatalf("anthropic still seeds the disabled model %q", seed)
	}
	if view.Model == "" {
		t.Fatal("anthropic fell back to nothing instead of the next available model")
	}
}

// TestRuntimeOptions_DropsDisabledRuntimes covers the wizard's runtime picker,
// which used to be eleven hardcoded rows that no opt-out could reach.
func TestRuntimeOptions_DropsDisabledRuntimes(t *testing.T) {
	if got, want := len(runtimeOptions()), len(ai.AllRuntimes()); got != want {
		t.Fatalf("runtimeOptions() = %d rows, want one per runtime (%d)", got, want)
	}

	api.SetDisabled(api.NewDisabledSet([]string{"cmux"}, nil, nil, nil, nil))
	t.Cleanup(func() { api.SetDisabled(api.DisabledSet{}) })

	for _, option := range runtimeOptions() {
		if strings.HasSuffix(option.Value, "/cmux") {
			t.Errorf("runtimeOptions() still offers %q with cmux disabled", option.Value)
		}
	}
	if got, want := len(runtimeOptions()), len(ai.AllRuntimes())-2; got != want {
		t.Fatalf("runtimeOptions() = %d rows with cmux disabled, want %d", got, want)
	}
}

func TestRuntimeLabel_DerivesFromIDs(t *testing.T) {
	cases := map[string]string{
		"claude/api":   "Claude API",
		"claude/cmux":  "Claude cmux",
		"codex/cli":    "Codex CLI",
		"codex/agent":  "Codex Agent",
		"deepseek/api": "DeepSeek API",
		"gemini/cli":   "Gemini CLI",
	}
	for input, want := range cases {
		family, mode, _ := strings.Cut(input, "/")
		if got := runtimeLabel(family, mode); got != want {
			t.Errorf("runtimeLabel(%s) = %q, want %q", input, got, want)
		}
	}
}

func TestEffortHuhOptionsForModel(t *testing.T) {
	luna := effortHuhOptionsFor(ai.OpenAI, ai.ModeAgent, "luna")
	for _, option := range luna {
		if option.Value == "ultra" {
			t.Fatalf("Luna options should not include ultra: %+v", luna)
		}
	}
	sol := effortHuhOptionsFor(ai.OpenAI, ai.ModeAgent, "sol")
	foundMax := false
	for _, option := range sol {
		foundMax = foundMax || option.Value == "max"
	}
	if !foundMax {
		t.Fatalf("Sol options should include max: %+v", sol)
	}
	noEffort := effortHuhOptionsFor(ai.DeepSeek, ai.ModeAPI, "deepseek-v4-pro")
	if len(noEffort) != 1 || noEffort[0].Value != "" {
		t.Fatalf("DeepSeek options = %+v, want only the runtime default", noEffort)
	}
}

func TestValidators(t *testing.T) {
	if err := validateFloat(""); err != nil {
		t.Errorf("validateFloat(\"\") = %v, want nil (blank allowed)", err)
	}
	if err := validateFloat("1.5"); err != nil {
		t.Errorf("validateFloat(\"1.5\") = %v, want nil", err)
	}
	if err := validateFloat("nope"); err == nil {
		t.Error("validateFloat(\"nope\") = nil, want error")
	}
	if err := validateInt("4096"); err != nil {
		t.Errorf("validateInt(\"4096\") = %v, want nil", err)
	}
	if err := validateInt("4.5"); err == nil {
		t.Error("validateInt(\"4.5\") = nil, want error")
	}
	if err := validateDuration("120s"); err != nil {
		t.Errorf("validateDuration(\"120s\") = %v, want nil", err)
	}
	if err := validateDuration("forever"); err == nil {
		t.Error("validateDuration(\"forever\") = nil, want error")
	}
}
