package provider

import (
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/claudeagent"
	"github.com/flanksource/captain/pkg/ai/provider/cmux"
	"github.com/flanksource/captain/pkg/ai/provider/genkit"
)

func init() {
	// API backends are served by Firebase Genkit (replaces the per-SDK providers).
	ai.RegisterProvider(ai.BackendAnthropic, func(cfg ai.Config) (ai.Provider, error) { return genkit.New(cfg) })
	ai.RegisterProvider(ai.BackendOpenAI, func(cfg ai.Config) (ai.Provider, error) { return genkit.New(cfg) })
	ai.RegisterProvider(ai.BackendGemini, func(cfg ai.Config) (ai.Provider, error) { return genkit.New(cfg) })
	// DeepSeek exposes an OpenAI-compatible API; genkit serves it via compat_oai
	// with a custom base URL (see pluginFor).
	ai.RegisterProvider(ai.BackendDeepSeek, func(cfg ai.Config) (ai.Provider, error) { return genkit.New(cfg) })

	// Claude Agent SDK as a supervised TS process over JSON-RPC replaces the
	// one-shot `claude -p` path; claude-code-* models route here too.
	ai.RegisterProvider(ai.BackendClaudeAgent, func(cfg ai.Config) (ai.Provider, error) { return claudeagent.New(cfg) })
	ai.RegisterProvider(ai.BackendClaudeCLI, func(cfg ai.Config) (ai.Provider, error) { return claudeagent.New(cfg) })

	// Codex via `codex app-server` (JSON-RPC) replaces `codex exec --json`.
	ai.RegisterProvider(ai.BackendCodexCLI, func(cfg ai.Config) (ai.Provider, error) { return NewCodexAppServer(cfg.Model.Name) })

	// cmux drives an interactive claude/codex TUI inside a tmux/cmux surface,
	// tailing the session JSONL; the same provider serves both agents (it reads
	// the agent from cfg.Model.Backend).
	ai.RegisterProvider(ai.BackendClaudeCmux, func(cfg ai.Config) (ai.Provider, error) { return cmux.New(cfg) })
	ai.RegisterProvider(ai.BackendCodexCmux, func(cfg ai.Config) (ai.Provider, error) { return cmux.New(cfg) })

	// Gemini CLI is unchanged.
	ai.RegisterProvider(ai.BackendGeminiCLI, func(cfg ai.Config) (ai.Provider, error) { return NewGeminiCLI(cfg.Model.Name), nil })
}
