package provider

import (
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/claudeagent"
	"github.com/flanksource/captain/pkg/ai/provider/cmux"
	"github.com/flanksource/captain/pkg/ai/provider/genkit"
	"github.com/flanksource/captain/pkg/ai/provider/jev"
	"github.com/flanksource/captain/pkg/ai/provider/openai"

	// Register the sandbox adapters so api.NewSandbox can construct them for
	// the CLI exec seam (newSandboxedCommand).
	_ "github.com/flanksource/captain/pkg/sandbox/adapter"
)

// The registry is keyed on (provider, mode), with one adapter per cell.
func init() {
	ai.RegisterRuntimeProbe(ai.Anthropic, ai.ModeAgent, claudeagent.ProbeRuntime)

	// DeepSeek uses Genkit's compat_oai plugin with a custom base URL.
	genkitFactory := func(cfg ai.Config) (ai.Provider, error) { return genkit.New(cfg) }
	for _, p := range []*ai.ModelProvider{ai.Anthropic, ai.Google, ai.DeepSeek} {
		ai.RegisterProvider(ai.RuntimeOf(p, ai.ModeAPI), genkitFactory)
	}
	ai.RegisterProvider(ai.RuntimeOf(ai.OpenAI, ai.ModeAPI), func(cfg ai.Config) (ai.Provider, error) {
		return openai.New(cfg)
	})
	ai.RegisterProvider(ai.RuntimeOf(ai.Jev, ai.ModeAPI), func(cfg ai.Config) (ai.Provider, error) {
		return jev.New(cfg)
	})

	// cmux drives an interactive claude/codex TUI inside a tmux/cmux surface,
	// tailing the session JSONL; one provider serves both families (it reads the
	// agent from the model's provider).
	cmuxFactory := func(cfg ai.Config) (ai.Provider, error) { return cmux.New(cfg) }
	for _, p := range []*ai.ModelProvider{ai.Anthropic, ai.OpenAI} {
		ai.RegisterProvider(ai.RuntimeOf(p, ai.ModeCmux), cmuxFactory)
	}

	ai.RegisterProvider(ai.RuntimeOf(ai.Anthropic, ai.ModeAgent), func(cfg ai.Config) (ai.Provider, error) {
		return claudeagent.New(cfg)
	})
	ai.RegisterProvider(ai.RuntimeOf(ai.OpenAI, ai.ModeAgent), func(cfg ai.Config) (ai.Provider, error) {
		return NewCodexAppServer(cfg)
	})

	ai.RegisterProvider(ai.RuntimeOf(ai.Anthropic, ai.ModeCLI), func(cfg ai.Config) (ai.Provider, error) {
		provider := NewClaudeCLI(cfg.Model.Name)
		provider.sandbox = cfg.ResolvedSandbox()
		return provider, nil
	})
	ai.RegisterProvider(ai.RuntimeOf(ai.OpenAI, ai.ModeCLI), func(cfg ai.Config) (ai.Provider, error) {
		return NewCodexCLI(cfg), nil
	})
	ai.RegisterProvider(ai.RuntimeOf(ai.Google, ai.ModeCLI), func(cfg ai.Config) (ai.Provider, error) {
		provider := NewGeminiCLI(cfg.Model.Name)
		provider.sandbox = cfg.ResolvedSandbox()
		return provider, nil
	})
}
