package ai

// ModelDef defines a model with its capabilities and pricing.
type ModelDef struct {
	ID            string  // Model identifier for API calls
	Name          string  // Human-readable display name
	Backend       Backend // Which backend/provider to use
	Reasoning     bool    // Whether the model supports extended thinking
	ContextWindow int     // Maximum context window in tokens
	MaxTokens     int     // Maximum output tokens
	InputPrice    float64 // Cost per million input tokens (USD)
	OutputPrice   float64 // Cost per million output tokens (USD)
	CacheRead     float64 // Cost per million cache-read tokens (USD)
	CacheWrite    float64 // Cost per million cache-write tokens (USD)
	Default       bool    // Whether this is the default model for its backend
}

// DefaultModels returns the built-in model catalog for all providers.
// These are available without needing the OpenRouter pricing API.
func DefaultModels() []ModelDef {
	return defaultModels
}

// DefaultModel returns the default model for a given backend.
func DefaultModel(backend Backend) (ModelDef, bool) {
	for _, m := range defaultModels {
		if m.Backend == backend && m.Default {
			return m, true
		}
	}
	return ModelDef{}, false
}

// ModelsByBackend returns all models for a given backend.
func ModelsByBackend(backend Backend) []ModelDef {
	var result []ModelDef
	for _, m := range defaultModels {
		if m.Backend == backend {
			result = append(result, m)
		}
	}
	return result
}

// Pricing data sourced from pi-mono/packages/ai/src/models.generated.ts

var defaultModels = []ModelDef{
	// =========================================================================
	// Anthropic (API) — https://docs.anthropic.com/en/docs/about-claude/models
	// =========================================================================

	// Claude 4.6 — latest generation
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.30, CacheWrite: 3.75, Default: true},
	{ID: "claude-opus-4-6", Name: "Claude Opus 4.6", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 128000, InputPrice: 5.00, OutputPrice: 25.00, CacheRead: 0.50, CacheWrite: 6.25},

	// Claude 4.5
	{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5 (latest)", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
	{ID: "claude-sonnet-4-5-20250929", Name: "Claude Sonnet 4.5", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
	{ID: "claude-opus-4-5", Name: "Claude Opus 4.5 (latest)", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 5.00, OutputPrice: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	{ID: "claude-opus-4-5-20251101", Name: "Claude Opus 4.5", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 5.00, OutputPrice: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5 (latest)", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 1.00, OutputPrice: 5.00, CacheRead: 0.10, CacheWrite: 1.25},
	{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 1.00, OutputPrice: 5.00, CacheRead: 0.10, CacheWrite: 1.25},

	// Claude 4.0 / 4.1
	{ID: "claude-sonnet-4-0", Name: "Claude Sonnet 4 (latest)", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
	{ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
	{ID: "claude-opus-4-1", Name: "Claude Opus 4.1 (latest)", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 32000, InputPrice: 15.00, OutputPrice: 75.00, CacheRead: 1.50, CacheWrite: 18.75},
	{ID: "claude-opus-4-1-20250805", Name: "Claude Opus 4.1", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 32000, InputPrice: 15.00, OutputPrice: 75.00, CacheRead: 1.50, CacheWrite: 18.75},
	{ID: "claude-opus-4-0", Name: "Claude Opus 4 (latest)", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 32000, InputPrice: 15.00, OutputPrice: 75.00, CacheRead: 1.50, CacheWrite: 18.75},
	{ID: "claude-opus-4-20250514", Name: "Claude Opus 4", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 32000, InputPrice: 15.00, OutputPrice: 75.00, CacheRead: 1.50, CacheWrite: 18.75},

	// Claude 3.7
	{ID: "claude-3-7-sonnet-latest", Name: "Claude Sonnet 3.7 (latest)", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
	{ID: "claude-3-7-sonnet-20250219", Name: "Claude Sonnet 3.7", Backend: BackendAnthropic, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.30, CacheWrite: 3.75},

	// Claude 3.5
	{ID: "claude-3-5-sonnet-20241022", Name: "Claude Sonnet 3.5 v2", Backend: BackendAnthropic, Reasoning: false, ContextWindow: 200000, MaxTokens: 8192, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
	{ID: "claude-3-5-haiku-latest", Name: "Claude Haiku 3.5 (latest)", Backend: BackendAnthropic, Reasoning: false, ContextWindow: 200000, MaxTokens: 8192, InputPrice: 0.80, OutputPrice: 4.00, CacheRead: 0.08, CacheWrite: 1.00},
	{ID: "claude-3-5-haiku-20241022", Name: "Claude Haiku 3.5", Backend: BackendAnthropic, Reasoning: false, ContextWindow: 200000, MaxTokens: 8192, InputPrice: 0.80, OutputPrice: 4.00, CacheRead: 0.08, CacheWrite: 1.00},

	// Claude 3
	{ID: "claude-3-opus-20240229", Name: "Claude Opus 3", Backend: BackendAnthropic, Reasoning: false, ContextWindow: 200000, MaxTokens: 4096, InputPrice: 15.00, OutputPrice: 75.00, CacheRead: 1.50, CacheWrite: 18.75},
	{ID: "claude-3-haiku-20240307", Name: "Claude Haiku 3", Backend: BackendAnthropic, Reasoning: false, ContextWindow: 200000, MaxTokens: 4096, InputPrice: 0.25, OutputPrice: 1.25, CacheRead: 0.03, CacheWrite: 0.30},

	// =========================================================================
	// Google Gemini (API) — https://ai.google.dev/gemini-api/docs/models
	// =========================================================================

	// Gemini 3 Preview
	{ID: "gemini-3-pro-preview", Name: "Gemini 3 Pro Preview", Backend: BackendGemini, Reasoning: true, ContextWindow: 128000, MaxTokens: 64000},
	{ID: "gemini-3-flash-preview", Name: "Gemini 3 Flash", Backend: BackendGemini, Reasoning: true, ContextWindow: 128000, MaxTokens: 64000},

	// Gemini 2.5
	{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Backend: BackendGemini, Reasoning: false, ContextWindow: 128000, MaxTokens: 64000},
	{ID: "gemini-2.5-pro-preview-06-05", Name: "Gemini 2.5 Pro Preview 06-05", Backend: BackendGemini, Reasoning: true, ContextWindow: 1048576, MaxTokens: 65536, InputPrice: 1.25, OutputPrice: 10.00, CacheRead: 0.31},
	{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Backend: BackendGemini, Reasoning: true, ContextWindow: 1048576, MaxTokens: 65536, InputPrice: 0.30, OutputPrice: 2.50, CacheRead: 0.075, Default: true},
	{ID: "gemini-2.5-flash-lite", Name: "Gemini 2.5 Flash Lite", Backend: BackendGemini, Reasoning: true, ContextWindow: 1048576, MaxTokens: 65536, InputPrice: 0.10, OutputPrice: 0.40, CacheRead: 0.025},
	{ID: "gemini-flash-latest", Name: "Gemini Flash Latest", Backend: BackendGemini, Reasoning: true, ContextWindow: 1048576, MaxTokens: 65536, InputPrice: 0.30, OutputPrice: 2.50, CacheRead: 0.075},
	{ID: "gemini-flash-lite-latest", Name: "Gemini Flash-Lite Latest", Backend: BackendGemini, Reasoning: true, ContextWindow: 1048576, MaxTokens: 65536, InputPrice: 0.10, OutputPrice: 0.40, CacheRead: 0.025},

	// Gemini 2.0
	{ID: "gemini-2.0-flash", Name: "Gemini 2.0 Flash", Backend: BackendGemini, Reasoning: false, ContextWindow: 1048576, MaxTokens: 8192, InputPrice: 0.10, OutputPrice: 0.40, CacheRead: 0.025},
	{ID: "gemini-2.0-flash-lite", Name: "Gemini 2.0 Flash Lite", Backend: BackendGemini, Reasoning: false, ContextWindow: 1048576, MaxTokens: 8192, InputPrice: 0.075, OutputPrice: 0.30},

	// Gemini 1.5
	{ID: "gemini-1.5-pro", Name: "Gemini 1.5 Pro", Backend: BackendGemini, Reasoning: false, ContextWindow: 1000000, MaxTokens: 8192, InputPrice: 1.25, OutputPrice: 5.00, CacheRead: 0.3125},
	{ID: "gemini-1.5-flash", Name: "Gemini 1.5 Flash", Backend: BackendGemini, Reasoning: false, ContextWindow: 1000000, MaxTokens: 8192, InputPrice: 0.075, OutputPrice: 0.30, CacheRead: 0.01875},
	{ID: "gemini-1.5-flash-8b", Name: "Gemini 1.5 Flash-8B", Backend: BackendGemini, Reasoning: false, ContextWindow: 1000000, MaxTokens: 8192, InputPrice: 0.0375, OutputPrice: 0.15, CacheRead: 0.01},

	// =========================================================================
	// OpenAI (via Codex CLI or API) — https://platform.openai.com/docs/models
	// =========================================================================

	// GPT-5.3
	{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 1.75, OutputPrice: 14.00, CacheRead: 0.175, Default: true},
	{ID: "gpt-5.3-codex-spark", Name: "GPT-5.3 Codex Spark", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 128000, MaxTokens: 32000, InputPrice: 1.75, OutputPrice: 14.00, CacheRead: 0.175},

	// GPT-5.2
	{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 1.75, OutputPrice: 14.00, CacheRead: 0.175},
	{ID: "gpt-5.2", Name: "GPT-5.2", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 1.75, OutputPrice: 14.00, CacheRead: 0.175},
	{ID: "gpt-5.2-pro", Name: "GPT-5.2 Pro", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 21.00, OutputPrice: 168.00},

	// GPT-5.1
	{ID: "gpt-5.1-codex-max", Name: "GPT-5.1 Codex Max", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 1.25, OutputPrice: 10.00, CacheRead: 0.125},
	{ID: "gpt-5.1-codex-mini", Name: "GPT-5.1 Codex Mini", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 0.25, OutputPrice: 2.00, CacheRead: 0.025},
	{ID: "gpt-5.1", Name: "GPT-5.1", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 1.25, OutputPrice: 10.00, CacheRead: 0.13},

	// GPT-5
	{ID: "gpt-5", Name: "GPT-5", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 1.25, OutputPrice: 10.00, CacheRead: 0.125},
	{ID: "gpt-5-codex", Name: "GPT-5 Codex", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 1.25, OutputPrice: 10.00, CacheRead: 0.125},
	{ID: "gpt-5-mini", Name: "GPT-5 Mini", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 0.25, OutputPrice: 2.00, CacheRead: 0.025},
	{ID: "gpt-5-nano", Name: "GPT-5 Nano", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 128000, InputPrice: 0.05, OutputPrice: 0.40, CacheRead: 0.005},
	{ID: "gpt-5-pro", Name: "GPT-5 Pro", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 400000, MaxTokens: 272000, InputPrice: 15.00, OutputPrice: 120.00},

	// GPT-4.1
	{ID: "gpt-4.1", Name: "GPT-4.1", Backend: BackendCodexCLI, Reasoning: false, ContextWindow: 1047576, MaxTokens: 32768, InputPrice: 2.00, OutputPrice: 8.00, CacheRead: 0.50},
	{ID: "gpt-4.1-mini", Name: "GPT-4.1 Mini", Backend: BackendCodexCLI, Reasoning: false, ContextWindow: 1047576, MaxTokens: 32768, InputPrice: 0.40, OutputPrice: 1.60, CacheRead: 0.10},
	{ID: "gpt-4.1-nano", Name: "GPT-4.1 Nano", Backend: BackendCodexCLI, Reasoning: false, ContextWindow: 1047576, MaxTokens: 32768, InputPrice: 0.10, OutputPrice: 0.40, CacheRead: 0.03},

	// GPT-4o
	{ID: "gpt-4o", Name: "GPT-4o", Backend: BackendCodexCLI, Reasoning: false, ContextWindow: 128000, MaxTokens: 16384, InputPrice: 2.50, OutputPrice: 10.00, CacheRead: 1.25},
	{ID: "gpt-4o-mini", Name: "GPT-4o Mini", Backend: BackendCodexCLI, Reasoning: false, ContextWindow: 128000, MaxTokens: 16384, InputPrice: 0.15, OutputPrice: 0.60, CacheRead: 0.08},

	// o-series reasoning models
	{ID: "o4-mini", Name: "o4-mini", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000, InputPrice: 1.10, OutputPrice: 4.40, CacheRead: 0.28},
	{ID: "o4-mini-deep-research", Name: "o4-mini Deep Research", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000, InputPrice: 2.00, OutputPrice: 8.00, CacheRead: 0.50},
	{ID: "o3", Name: "o3", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000, InputPrice: 2.00, OutputPrice: 8.00, CacheRead: 0.50},
	{ID: "o3-pro", Name: "o3 Pro", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000, InputPrice: 20.00, OutputPrice: 80.00},
	{ID: "o3-mini", Name: "o3-mini", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000, InputPrice: 1.10, OutputPrice: 4.40, CacheRead: 0.55},
	{ID: "o3-deep-research", Name: "o3 Deep Research", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000, InputPrice: 10.00, OutputPrice: 40.00, CacheRead: 2.50},
	{ID: "o1", Name: "o1", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000, InputPrice: 15.00, OutputPrice: 60.00, CacheRead: 7.50},
	{ID: "o1-pro", Name: "o1 Pro", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000, InputPrice: 150.00, OutputPrice: 600.00},

	// Codex
	{ID: "codex-mini-latest", Name: "Codex Mini", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 100000, InputPrice: 1.50, OutputPrice: 6.00, CacheRead: 0.375},

	// =========================================================================
	// Claude CLI (claude-code-* prefix)
	// =========================================================================
	{ID: "claude-code-sonnet", Name: "Claude Code Sonnet", Backend: BackendClaudeCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00, Default: true},
	{ID: "claude-code-sonnet-4-6", Name: "Claude Code Sonnet 4.6", Backend: BackendClaudeCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00},
	{ID: "claude-code-opus", Name: "Claude Code Opus", Backend: BackendClaudeCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 32000, InputPrice: 15.00, OutputPrice: 75.00},
	{ID: "claude-code-opus-4-6", Name: "Claude Code Opus 4.6", Backend: BackendClaudeCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 128000, InputPrice: 5.00, OutputPrice: 25.00},
	{ID: "claude-code-haiku", Name: "Claude Code Haiku", Backend: BackendClaudeCLI, Reasoning: true, ContextWindow: 200000, MaxTokens: 64000, InputPrice: 1.00, OutputPrice: 5.00},

	// =========================================================================
	// Gemini CLI (gemini-cli-* prefix)
	// =========================================================================
	{ID: "gemini-cli-2.5-flash", Name: "Gemini CLI 2.5 Flash", Backend: BackendGeminiCLI, Reasoning: true, ContextWindow: 1048576, MaxTokens: 65536, InputPrice: 0.30, OutputPrice: 2.50, Default: true},
	{ID: "gemini-cli-2.5-pro", Name: "Gemini CLI 2.5 Pro", Backend: BackendGeminiCLI, Reasoning: false, ContextWindow: 128000, MaxTokens: 64000},
	{ID: "gemini-cli-3-flash", Name: "Gemini CLI 3 Flash", Backend: BackendGeminiCLI, Reasoning: true, ContextWindow: 128000, MaxTokens: 64000},
	{ID: "gemini-cli-3-pro", Name: "Gemini CLI 3 Pro Preview", Backend: BackendGeminiCLI, Reasoning: true, ContextWindow: 128000, MaxTokens: 64000},
	{ID: "gemini-cli-2.0-flash", Name: "Gemini CLI 2.0 Flash", Backend: BackendGeminiCLI, Reasoning: false, ContextWindow: 1048576, MaxTokens: 8192, InputPrice: 0.10, OutputPrice: 0.40},

	// =========================================================================
	// xAI Grok — https://docs.x.ai/docs/models
	// =========================================================================
	{ID: "grok-4", Name: "Grok 4", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 256000, MaxTokens: 64000, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.75},
	{ID: "grok-4-fast", Name: "Grok 4 Fast", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 2000000, MaxTokens: 30000, InputPrice: 0.20, OutputPrice: 0.50, CacheRead: 0.05},
	{ID: "grok-4-1-fast", Name: "Grok 4.1 Fast", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 2000000, MaxTokens: 30000, InputPrice: 0.20, OutputPrice: 0.50, CacheRead: 0.05},
	{ID: "grok-code-fast-1", Name: "Grok Code Fast 1", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 128000, MaxTokens: 64000},
	{ID: "grok-3", Name: "Grok 3", Backend: BackendCodexCLI, Reasoning: false, ContextWindow: 131072, MaxTokens: 8192, InputPrice: 3.00, OutputPrice: 15.00, CacheRead: 0.75},
	{ID: "grok-3-mini", Name: "Grok 3 Mini", Backend: BackendCodexCLI, Reasoning: true, ContextWindow: 131072, MaxTokens: 8192, InputPrice: 0.30, OutputPrice: 0.50, CacheRead: 0.075},
}
