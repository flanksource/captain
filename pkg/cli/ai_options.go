package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

func loadSavedConfig() (captainconfig.Config, error) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return captainconfig.Config{}, fmt.Errorf("load Captain configuration: %w", err)
	}
	return cfg, nil
}

// AIProviderOptions binds model selection plus the knobs that belong to the
// request rather than the model: the endpoint, the API key and the spend budget.
//
// The model flags themselves live in pkg/aiflags — a leaf any clicky CLI can embed
// without inheriting pkg/cli's ~1000 transitive packages. Embedding it here keeps
// captain's flag surface unchanged (clicky promotes embedded flags at any depth)
// while giving downstream repos the same parsing captain uses.
type AIProviderOptions struct {
	aiflags.ModelFlags

	APIKey  string `flag:"api-key" help:"API key (env: ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, GOOGLE_API_KEY, DEEPSEEK_API_KEY)"`
	APIURL  string `flag:"api-url" help:"Override the provider endpoint, e.g. a 'captain ai mock' URL. Required for the openai cli mode, which ignores OPENAI_BASE_URL when a ChatGPT credential is stored"`
	Budget  string `flag:"budget" help:"Max spend in USD, 0=unlimited"`
	Sandbox string `flag:"sandbox" help:"Sandbox for the run: off|native|docker|git-agent, or a configured Docker/Git Agent backend"`
}

// SandboxSelector trims the public sandbox mode or configured backend selector.
func (o AIProviderOptions) SandboxSelector() string {
	return strings.TrimSpace(o.Sandbox)
}

// BudgetUSD parses --budget, failing loud on malformed input.
func (o AIProviderOptions) BudgetUSD() (float64, error) {
	return parseFloatFlag("budget", o.Budget)
}

// parseFloatFlag parses a numeric string flag, returning a descriptive error
// instead of silently coercing malformed input to zero.
func parseFloatFlag(name, val string) (float64, error) {
	if val == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid --%s %q: %w", name, val, err)
	}
	return f, nil
}

// schemaRepairConfig reads the `model:`/`mode:` pair out of ~/.captain.yaml.
// `mode:` names the mechanism (api|agent|cli|cmux); the provider follows from the
// model name, and the pair is resolved where the repair provider is built.
func schemaRepairConfig(saved captainconfig.SchemaRepairDefaults) api.SchemaRepairConfig {
	return api.SchemaRepairConfig{
		Model: api.Model{
			Name: strings.TrimSpace(saved.Model),
			Mode: api.RuntimeMode(strings.TrimSpace(saved.Mode)),
		},
		Prompt: strings.TrimSpace(saved.Prompt),
	}
}

// AIRuntimeOptions binds the per-invocation knobs every AI command shares —
// model selection (via embedded AIProviderOptions), generation parameters
// (max tokens, temperature, timeout, reasoning), permission/sandbox toggles
// (edit, allowed/disallowed tools, permission mode), and ambient-context
// toggles (mcp/hooks/skills/user/project/memory/bare). It deliberately
// omits the user-prompt fields so non-prompt commands (e.g. gavel's lint
// --ai-fix loop) can embed it without inheriting a required --prompt flag.
//
// AIPromptOptions embeds this struct and adds Prompt/System/AppendSystem/
// NoStream on top.
type AIRuntimeOptions struct {
	AIProviderOptions
	ExplicitFields api.FieldPresence `flag:"-" json:"-" yaml:"-"`

	// Effort and Temperature are NOT here: they describe the model and so live on
	// the embedded aiflags.ModelFlags, promoted through AIProviderOptions.
	// Redeclaring them would bind --effort twice and panic cobra at init.
	MaxTokens int    `flag:"max-tokens" help:"Maximum output tokens (unset = saved default or 4096; 0 = provider default)"`
	MaxTurns  int    `flag:"max-turns" help:"Max agent turns 0-100, 0 = provider default (agent mode)"`
	Resume    string `flag:"resume" help:"Resume an existing session by id (agent and cli modes)"`

	Edit            bool     `flag:"edit" help:"Safe defaults: acceptEdits + Read/Edit/Write/Glob/Grep allowlist"`
	AllowedTools    []string `flag:"allowed-tools" help:"Override --edit's built-in allowlist (claude only)"`
	DisallowedTools []string `flag:"disallowed-tools" help:"Tools to deny (claude only)"`
	PermissionMode  string   `flag:"permission-mode" help:"acceptEdits|auto|bypassPermissions|default|plan"`

	NoMCP     bool     `flag:"no-mcp" help:"Disable all MCP servers"`
	NoHooks   bool     `flag:"no-hooks" help:"Skip hooks"`
	NoSkills  bool     `flag:"no-skills" help:"Disable slash commands"`
	SkillDirs []string `flag:"skill-dir" help:"Additional skill/plugin directory (repeatable)"`
	NoUser    bool     `flag:"no-user" help:"Skip user-level settings"`
	NoProject bool     `flag:"no-project" help:"Skip project-level settings"`
	NoMemory  bool     `flag:"no-memory" help:"Skip auto-memory and CLAUDE.md"`
	Bare      bool     `flag:"bare" help:"Skip hooks, skills, memory, and ambient settings"`
}

var validPermissionModes = []string{"acceptEdits", "auto", "bypassPermissions", "default", "plan"}

func validatePermissionMode(s string) error {
	if s == "" {
		return nil
	}
	for _, m := range validPermissionModes {
		if s == m {
			return nil
		}
	}
	return fmt.Errorf("invalid --permission-mode %q (valid: %s)", s, strings.Join(validPermissionModes, "|"))
}

type AIPromptOptions struct {
	AIRuntimeOptions

	// File is a positional .prompt template path rendered through pkg/ai/prompt.
	// The frontmatter sets model + any ai.Request option; the body is the prompt.
	File         string   `args:"true" help:"Path to a .prompt template to render"`
	Prompt       string   `flag:"prompt" clicky:"cli-file-read" help:"Prompt text, or @file to load and render a .prompt template" short:"p"`
	System       string   `flag:"system" help:"System prompt" short:"s"`
	AppendSystem string   `flag:"append-system" help:"Append text to the default system prompt"`
	Var          []string `flag:"var" help:"Template variable key=value (repeatable)" short:"V"`
	Attach       []string `flag:"attach" help:"Attach a local path or URL (repeatable; RFC 4180 comma-separated values allowed)" short:"A"`
	MultiModels  []string `flag:"multi-models" help:"Run prompt once per runtime selector in parallel, e.g. cli:sonnet-5,cmux:opus (repeatable; comma-separated allowed)" short:"M"`
	Timeout      string   `flag:"timeout" help:"Request timeout (default 120s; a relocating sandbox waits for the remote agent instead)"`
	NoStream     bool     `flag:"no-stream" help:"Disable streaming; print only the final text to stdout"`

	// RuntimeProfile is the catalog profile (id or name) `captain prompt
	// run|render --runtime-profile` layers beneath the frontmatter. It is not a
	// flag here: the deprecated `captain ai prompt` alias does not grow it.
	RuntimeProfile string
}

type AIPromptResult struct {
	Text             string         `json:"text" pretty:"label=Response"`
	StructuredOutput map[string]any `json:"structuredOutput,omitempty" pretty:"-"`
	Model            string         `json:"model" pretty:"label=Model"`
	Provider         string         `json:"provider" pretty:"label=Provider"`
	Mode             string         `json:"mode" pretty:"label=Mode"`
	Dir              string         `json:"dir,omitempty" pretty:"label=Dir"`
	SessionID        string         `json:"sessionId,omitempty" pretty:"label=Session"`
	HistoryFile      string         `json:"historyFile,omitempty" pretty:"label=History"`
	Input            ai.Request     `json:"input" pretty:"-"`
	InputTokens      int            `json:"inputTokens" pretty:"label=Input Tokens"`
	Output           int            `json:"outputTokens" pretty:"label=Output Tokens"`
	CostUSD          float64        `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	Duration         string         `json:"duration" pretty:"label=Duration"`
}
