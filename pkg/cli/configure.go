package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/captainconfig"
)

type ConfigureOptions struct{}

type ConfigureResult struct {
	Path            string `json:"path" pretty:"label=Saved To"`
	Backend         string `json:"backend" pretty:"label=Backend"`
	Model           string `json:"model" pretty:"label=Model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty" pretty:"label=Reasoning Effort"`
	BudgetUSD       string `json:"budgetUSD,omitempty" pretty:"label=Budget (USD)"`
	MaxTokens       string `json:"maxTokens,omitempty" pretty:"label=Max Tokens"`
	Timeout         string `json:"timeout,omitempty" pretty:"label=Timeout"`
	Toggles         string `json:"toggles" pretty:"label=Enabled"`
}

const (
	toggleCaching = "caching"
	toggleMCP     = "mcp"
	toggleHooks   = "hooks"
	toggleSkills  = "skills"
	toggleUser    = "user-settings"
	toggleProject = "project-settings"
	toggleMemory  = "memory"
)

// allToggles is the canonical order presented in the wizard. Stable order keeps
// the form predictable and the test below straightforward.
var allToggles = []string{toggleCaching, toggleMCP, toggleHooks, toggleSkills, toggleUser, toggleProject, toggleMemory}

func RunConfigure(opts ConfigureOptions) (any, error) {
	current, _, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}

	backend := defaultString(current.AI.Backend, string(ai.BackendAnthropic))
	model := defaultString(current.AI.Model, defaultModelFor(ai.Backend(backend)))
	effort := defaultString(current.AI.ReasoningEffort, "high")
	budget := floatToInput(current.AI.BudgetUSD)
	maxTokens := intToInput(current.AI.MaxTokens)
	timeout := defaultString(current.AI.Timeout, "120s")
	enabled := togglesFromConfig(current.AI)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Default backend").
				Description("Used when --backend is not passed. Determines which models are available below.").
				Options(backendOptions()...).
				Value(&backend),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Default model").
				Description("Used when --model is not passed. List is filtered by the backend chosen above.").
				OptionsFunc(func() []huh.Option[string] {
					return modelOptionsFor(ai.Backend(backend))
				}, &backend).
				Value(&model),
			huh.NewSelect[string]().
				Title("Reasoning effort").
				Description("Honoured by codex-cli and the API backends (thinking budget); CLI wrappers may ignore.").
				Options(
					huh.NewOption("low", "low"),
					huh.NewOption("medium", "medium"),
					huh.NewOption("high", "high"),
				).
				Value(&effort),
			huh.NewInput().
				Title("Budget (USD)").
				Description("Stop after spending this much; 0 disables.").
				Value(&budget).
				Validate(validateFloat),
			huh.NewInput().
				Title("Max tokens").
				Description("Maximum output tokens per request; 0 lets the provider decide.").
				Value(&maxTokens).
				Validate(validateInt),
			huh.NewInput().
				Title("Request timeout").
				Description(`Go duration string, e.g. "120s", "5m".`).
				Value(&timeout).
				Validate(validateDuration),
		),
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Defaults to ENABLE").
				Description("Selected = enabled by default. Per call, --no-mcp/--no-hooks/etc. disable further; re-enable a disabled default by rerunning configure.").
				Options(toggleHuhOptions()...).
				Value(&enabled),
		),
	)

	if err := form.Run(); err != nil {
		return nil, err
	}

	cfg := buildConfigFromForm(formInputs{
		Backend:         backend,
		Model:           model,
		ReasoningEffort: effort,
		BudgetUSD:       budget,
		MaxTokens:       maxTokens,
		Timeout:         timeout,
		Enabled:         enabled,
	})
	if err := captainconfig.Save(cfg); err != nil {
		return nil, err
	}

	path, _ := captainconfig.Path()
	return ConfigureResult{
		Path:            path,
		Backend:         cfg.AI.Backend,
		Model:           cfg.AI.Model,
		ReasoningEffort: cfg.AI.ReasoningEffort,
		BudgetUSD:       budget,
		MaxTokens:       maxTokens,
		Timeout:         cfg.AI.Timeout,
		Toggles:         strings.Join(enabled, ", "),
	}, nil
}

// formInputs is the raw string/slice output of the huh form. Keeping it in a
// struct makes buildConfigFromForm a pure function we can unit-test without a
// TTY.
type formInputs struct {
	Backend         string
	Model           string
	ReasoningEffort string
	BudgetUSD       string
	MaxTokens       string
	Timeout         string
	Enabled         []string // multi-select positive phrasing
}

func buildConfigFromForm(in formInputs) captainconfig.Config {
	budget, _ := strconv.ParseFloat(strings.TrimSpace(in.BudgetUSD), 64)
	maxTokens, _ := strconv.Atoi(strings.TrimSpace(in.MaxTokens))

	enabled := make(map[string]bool, len(in.Enabled))
	for _, e := range in.Enabled {
		enabled[e] = true
	}

	return captainconfig.Config{
		AI: captainconfig.AIDefaults{
			Backend:         in.Backend,
			Model:           in.Model,
			ReasoningEffort: in.ReasoningEffort,
			BudgetUSD:       budget,
			MaxTokens:       maxTokens,
			Timeout:         strings.TrimSpace(in.Timeout),
			NoCache:         !enabled[toggleCaching],
			NoMCP:           !enabled[toggleMCP],
			NoHooks:         !enabled[toggleHooks],
			NoSkills:        !enabled[toggleSkills],
			NoUser:          !enabled[toggleUser],
			NoProject:       !enabled[toggleProject],
			NoMemory:        !enabled[toggleMemory],
		},
	}
}

func togglesFromConfig(a captainconfig.AIDefaults) []string {
	out := make([]string, 0, len(allToggles))
	checks := map[string]bool{
		toggleCaching: !a.NoCache,
		toggleMCP:     !a.NoMCP,
		toggleHooks:   !a.NoHooks,
		toggleSkills:  !a.NoSkills,
		toggleUser:    !a.NoUser,
		toggleProject: !a.NoProject,
		toggleMemory:  !a.NoMemory,
	}
	for _, t := range allToggles {
		if checks[t] {
			out = append(out, t)
		}
	}
	return out
}

func toggleHuhOptions() []huh.Option[string] {
	out := make([]huh.Option[string], len(allToggles))
	for i, t := range allToggles {
		out[i] = huh.NewOption(t, t)
	}
	return out
}

func backendOptions() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("Anthropic API", string(ai.BackendAnthropic)),
		huh.NewOption("Google Gemini API", string(ai.BackendGemini)),
		huh.NewOption("OpenAI API", string(ai.BackendOpenAI)),
		huh.NewOption("Claude CLI", string(ai.BackendClaudeCLI)),
		huh.NewOption("Claude Agent (SDK)", string(ai.BackendClaudeAgent)),
		huh.NewOption("Claude cmux", string(ai.BackendClaudeCmux)),
		huh.NewOption("Codex CLI", string(ai.BackendCodexCLI)),
		huh.NewOption("Codex cmux", string(ai.BackendCodexCmux)),
		huh.NewOption("Gemini CLI", string(ai.BackendGeminiCLI)),
	}
}

// modelOptionsFor renders the chosen backend's models as huh select options.
// CLI/agent backends authenticate internally, so their models come from the
// static catalog (no API key). API backends fetch the live /v1/models
// catalogue — the only source of truth, with no static fallback: if the key is
// missing or the call fails, the picker shows a single sentinel row carrying
// the error so the user can fix their environment without dropping out of the
// form.
func modelOptionsFor(b ai.Backend) []huh.Option[string] {
	if b.Kind() == "cli" {
		return modelHuhOptions(agentCatalogModels(b))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models, err := ai.ListModels(ctx, b)
	if err != nil {
		return []huh.Option[string]{huh.NewOption(fmt.Sprintf("(no models: %v)", err), "")}
	}

	// Drop legacy / non-chat IDs (Grok 3 mini, gpt-3.5, dall-e, ...) so the
	// picker shows only models worth defaulting to. Shared with `captain ai
	// models` so both surfaces stay consistent. The catalog used for CLI
	// backends is already curated, so this filter applies only to the raw
	// live list.
	filtered := make([]ai.ModelDef, 0, len(models))
	for _, m := range models {
		if isLegacyModelID(m.ID) {
			continue
		}
		filtered = append(filtered, m)
	}
	return modelHuhOptions(filtered)
}

// modelHuhOptions renders a (pre-sorted) model list as picker options, falling
// back to a single disabled sentinel row when the list is empty.
func modelHuhOptions(models []ai.ModelDef) []huh.Option[string] {
	if len(models) == 0 {
		return []huh.Option[string]{huh.NewOption("(no models available)", "")}
	}
	out := make([]huh.Option[string], 0, len(models))
	for _, m := range models {
		out = append(out, huh.NewOption(m.Name, m.ID))
	}
	return out
}

// defaultModelFor returns a hard-coded picker default per backend that seeds the
// form. CLI/agent backends use the catalog slug their picker actually lists
// (agentCatalogModels) so the seeded default is a selectable option. API
// backends have no "default" flag on /v1/models, so we use the most-current id
// we expect each provider to keep stable; the user can pick anything else.
func defaultModelFor(b ai.Backend) string {
	switch b {
	case ai.BackendAnthropic:
		return "claude-sonnet-4-5"
	case ai.BackendClaudeCLI, ai.BackendClaudeAgent:
		return "claude-agent-sonnet"
	case ai.BackendOpenAI:
		return "gpt-5.5"
	case ai.BackendCodexCLI:
		return "gpt-5-codex"
	case ai.BackendGemini, ai.BackendGeminiCLI:
		return "gemini-3.5-flash"
	}
	return ""
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func floatToInput(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func intToInput(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}

func validateFloat(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return fmt.Errorf("must be a number (got %q)", s)
	}
	return nil
}

func validateInt(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if _, err := strconv.Atoi(s); err != nil {
		return fmt.Errorf("must be an integer (got %q)", s)
	}
	return nil
}

func validateDuration(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if _, err := time.ParseDuration(s); err != nil {
		return fmt.Errorf("must be a duration like 120s or 5m (got %q)", s)
	}
	return nil
}
