package cli

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/captainconfig"
)

func TestBuildConfigFromForm_TogglesInvert(t *testing.T) {
	in := formInputs{
		Backend:         "anthropic",
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
			Backend:         "anthropic",
			Model:           "claude-sonnet-4-6",
			ReasoningEffort: "medium",
			BudgetUSD:       1.5,
			MaxTokens:       8192,
			Timeout:         "180s",
			NoCache:         false,
			NoMCP:           false,
			NoHooks:         false,
			NoSkills:        true,
			NoUser:          true,
			NoProject:       true,
			NoMemory:        true,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildConfigFromForm()\n got  = %+v\n want = %+v", got, want)
	}
}

func TestBuildConfigFromForm_BlankNumericInputs(t *testing.T) {
	got := buildConfigFromForm(formInputs{
		Backend: "openai",
		Model:   "gpt-5",
		Enabled: allToggles, // all enabled
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
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")

	for _, b := range []ai.Backend{ai.BackendAnthropic, ai.BackendOpenAI, ai.BackendGemini, ai.BackendDeepSeek} {
		opts := modelOptionsFor(b)
		if len(opts) != 1 {
			t.Errorf("backend=%s: expected 1 sentinel row, got %d (%+v)", b, len(opts), opts)
			continue
		}
		if !strings.Contains(opts[0].Key, "no models") {
			t.Errorf("backend=%s: sentinel row should carry an error message, got %+v", b, opts[0])
		}
	}
}

// TestModelOptionsFor_CLIBackendsUseCatalogWithoutKey verifies CLI/agent
// backends populate the picker from the static catalog with no API key set:
// they authenticate internally, so the wizard must never gate them on a key.
func TestModelOptionsFor_CLIBackendsUseCatalogWithoutKey(t *testing.T) {
	installTestCatalog(t)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	opts := modelOptionsFor(ai.BackendCodexCLI)
	if len(opts) != 1 {
		t.Fatalf("codex-cli picker = %+v, want a single catalog option", opts)
	}
	if opts[0].Value != "gpt-5.5" {
		t.Errorf("codex-cli option value = %q, want exact model gpt-5.5", opts[0].Value)
	}
}

func TestDefaultModelFor_HardcodedPerBackend(t *testing.T) {
	cases := map[ai.Backend]string{
		ai.BackendAnthropic:   "claude-sonnet-5",
		ai.BackendClaudeCLI:   "claude-sonnet-5",
		ai.BackendClaudeAgent: "claude-sonnet-5",
		ai.BackendOpenAI:      "gpt-5.5",
		ai.BackendCodexCLI:    "gpt-5.5",
		ai.BackendCodexAgent:  "gpt-5.5",
		ai.BackendGemini:      "gemini-3.5-flash",
		ai.BackendGeminiCLI:   "gemini-3.5-flash",
	}
	for b, want := range cases {
		if got := defaultModelFor(b); got != want {
			t.Errorf("defaultModelFor(%s) = %q, want %q", b, got, want)
		}
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
