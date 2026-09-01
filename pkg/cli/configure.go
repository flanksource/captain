package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	clickyrpc "github.com/flanksource/clicky/rpc"
	"github.com/flanksource/clicky/text"
)

type ConfigureOptions struct {
	Provider string               `flag:"provider" args:"true" help:"API provider to configure: anthropic|openai|gemini|deepseek"`
	Token    text.SensitiveString `flag:"token" hidden:"true" help:"Provider API token (prefer the secure interactive prompt)"`
	Test     bool                 `flag:"test" help:"Test the current or supplied token without saving it"`
	Mode     string               `flag:"mode" help:"Default runtime mode for this provider: api|agent|cli|cmux"`
	Model    string               `flag:"model" help:"Default model for this provider"`
	Effort   string               `flag:"effort" help:"Default reasoning effort, or default to use the model default"`
	Active   bool                 `flag:"active" help:"Use this provider for completely flagless runs"`
}

type ConfigureResult struct {
	Path            string `json:"path" pretty:"label=Saved To"`
	Provider        string `json:"provider" pretty:"label=Provider"`
	Mode            string `json:"mode" pretty:"label=Mode"`
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

func RunConfigure(ctx context.Context, opts ConfigureOptions) (any, error) {
	if _, ok := clickyrpc.RequestFromContext(ctx); ok {
		return nil, fmt.Errorf("configure is unavailable over generated RPC; use the guarded provider configuration APIs")
	}
	if strings.TrimSpace(opts.Provider) != "" {
		return runProviderConfigure(ctx, opts)
	}
	if !opts.Token.IsEmpty() || opts.Test || opts.Mode != "" || opts.Model != "" || opts.Effort != "" || opts.Active {
		return nil, fmt.Errorf("provider is required when using token or provider-default flags")
	}
	return runConfigureWizard()
}

func runConfigureWizard() (any, error) {
	current, _, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}

	activeProvider, known := api.ProviderByName(current.AI.ActiveProvider())
	if !known {
		return nil, fmt.Errorf("active provider %q is not a known provider (available: %s)", current.AI.ActiveProvider(), api.ProviderList())
	}
	currentDefaults, err := effectiveProviderDefaults(current.AI, activeProvider)
	if err != nil {
		return nil, err
	}
	runtimeKey := runtimeOptionKey(activeProvider.Name, currentDefaults.Mode)
	model := currentDefaults.Model
	effort := defaultString(currentDefaults.Effort, "high")
	budget := floatToInput(current.AI.BudgetUSD)
	maxTokens := intToInput(current.AI.MaxTokens)
	timeout := defaultString(current.AI.Timeout, "120s")
	enabled := togglesFromConfig(current.AI)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Default runtime").
				Description("Used when --mode is not passed. Determines which models are available below.").
				Options(runtimeOptions()...).
				Value(&runtimeKey),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Default model").
				Description("Used when --model is not passed. List is filtered by the runtime chosen above.").
				OptionsFunc(func() []huh.Option[string] {
					return modelOptionsFor(splitRuntimeOptionKey(runtimeKey))
				}, &runtimeKey).
				Value(&model),
			huh.NewSelect[string]().
				Title("Reasoning effort").
				Description("Honoured by the agent and API modes (thinking budget); CLI wrappers may ignore.").
				OptionsFunc(func() []huh.Option[string] {
					p, mode := splitRuntimeOptionKey(runtimeKey)
					return effortHuhOptionsFor(p, mode, model)
				}, []any{&runtimeKey, &model}).
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

	formProvider, formMode := splitRuntimeOptionKey(runtimeKey)
	cfg := buildConfigFromForm(formInputs{
		Provider:        formProvider,
		Mode:            string(formMode),
		Model:           model,
		ReasoningEffort: effort,
		BudgetUSD:       budget,
		MaxTokens:       maxTokens,
		Timeout:         timeout,
		Enabled:         enabled,
	})
	for provider, defaults := range current.AI.Providers {
		if _, exists := cfg.AI.Providers[provider]; !exists {
			cfg.AI.Providers[provider] = defaults
		}
	}
	cfg.Prompts = current.Prompts
	cfg.Attachments = current.Attachments
	if err := captainconfig.Save(cfg); err != nil {
		return nil, err
	}

	path, _ := captainconfig.Path()
	return ConfigureResult{
		Path:            path,
		Provider:        formProvider.Name,
		Mode:            string(formMode),
		Model:           model,
		ReasoningEffort: effort,
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
	Provider        *api.ModelProvider
	Mode            string
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
			DefaultProvider: in.Provider.Name,
			Providers: map[string]captainconfig.ProviderDefaults{
				in.Provider.Name: {Mode: in.Mode, Model: in.Model, ReasoningEffort: in.ReasoningEffort},
			},
			BudgetUSD: budget,
			MaxTokens: maxTokens,
			Timeout:   strings.TrimSpace(in.Timeout),
			NoCache:   !enabled[toggleCaching],
			NoMCP:     !enabled[toggleMCP],
			NoHooks:   !enabled[toggleHooks],
			NoSkills:  !enabled[toggleSkills],
			NoUser:    !enabled[toggleUser],
			NoProject: !enabled[toggleProject],
			NoMemory:  !enabled[toggleMemory],
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

// runtimeOptions renders the runtime descriptor as picker rows, dropping what
// the user switched off. It used to be eleven hand-written rows that neither
// tracked a new provider×mode pair nor honoured ai.disabled.
// runtimeOptions renders the enabled provider×mode cells. The option VALUE is a
// "provider/mode" form key, not a runtime id: the pair never collapses into one
// token on the wire or in config, only inside this form's widget state.
func runtimeOptions() []huh.Option[string] {
	out := make([]huh.Option[string], 0, len(api.AllRuntimes()))
	for _, family := range api.RuntimeCatalog() {
		for _, mode := range family.Modes {
			if mode.Disabled {
				continue
			}
			out = append(out, huh.NewOption(runtimeLabel(family.Family, mode.Mode), runtimeOptionKey(family.Provider, mode.Mode)))
		}
	}
	return out
}

func runtimeOptionKey(provider, mode string) string { return provider + "/" + mode }

func splitRuntimeOptionKey(key string) (*api.ModelProvider, api.RuntimeMode) {
	name, mode, _ := strings.Cut(key, "/")
	p, _ := api.ProviderByName(name)
	return p, api.RuntimeMode(mode)
}

// runtimeLabel renders "claude"+"api" as "Claude API". The descriptor carries
// ids only — a label field on the registry would be a second place for
// presentation to drift — so display text is derived here, at the one point
// that displays it.
func runtimeLabel(family, mode string) string {
	return brandCase(family) + " " + modeCase(mode)
}

func brandCase(family string) string {
	// The only family whose brand casing is not plain title case. Claude, Codex
	// and Gemini all derive correctly.
	if family == "deepseek" {
		return "DeepSeek"
	}
	return strings.ToUpper(family[:1]) + family[1:]
}

func modeCase(mode string) string {
	switch mode {
	case "api", "cli":
		return strings.ToUpper(mode)
	case "cmux":
		return mode
	default:
		return strings.ToUpper(mode[:1]) + mode[1:]
	}
}

// modelOptionsFor renders the chosen runtime's models as huh select options.
// The local transports authenticate internally, so their models come from the
// static catalog (no API key). The API mode fetches the live /v1/models
// catalogue — the only source of truth, with no static fallback: if the key is
// missing or the call fails, the picker shows a single sentinel row carrying
// the error so the user can fix their environment without dropping out of the
// form.
func modelOptionsFor(p *ai.ModelProvider, mode ai.RuntimeMode) []huh.Option[string] {
	if mode.Kind() == "cli" {
		return modelHuhOptions(agentCatalogModels(p, mode))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	models, err := ai.ListModels(ctx, p)
	if err != nil {
		return []huh.Option[string]{huh.NewOption(fmt.Sprintf("(no models: %v)", err), "")}
	}

	// Drop legacy / non-chat IDs (Grok 3 mini, gpt-3.5, dall-e, ...) so the
	// picker shows only models worth defaulting to. Shared with `captain ai
	// models` so both surfaces stay consistent. The catalog used for the local
	// transports is already curated, so this filter applies only to the raw
	// live list.
	filtered := make([]ai.ModelDef, 0, len(models))
	for _, m := range models {
		if ai.IsLegacyModelID(m.ID) {
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

// defaultModelFor seeds the form with the provider's current top pick for that
// mode. It was a hardcoded switch, which named models the catalog had since
// superseded and could seed one the user had disabled.
func defaultModelFor(p *ai.ModelProvider, mode ai.RuntimeMode) string {
	return aiflags.DefaultModelFor(p, mode)
}

func effortHuhOptions() []huh.Option[string] {
	return effortOptions(api.Disabled().EnabledEfforts())
}

func effortHuhOptionsFor(p *ai.ModelProvider, mode ai.RuntimeMode, model string) []huh.Option[string] {
	if supported, _, ok := ai.ModelEfforts(p, mode, model); ok {
		if len(supported) == 0 {
			return []huh.Option[string]{huh.NewOption("Runtime default", "")}
		}
		return effortOptions(supported)
	}
	return effortHuhOptions()
}

func effortOptions(efforts []api.Effort) []huh.Option[string] {
	out := make([]huh.Option[string], 0, len(efforts))
	for _, effort := range efforts {
		out = append(out, huh.NewOption(string(effort), string(effort)))
	}
	return out
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
