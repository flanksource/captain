package ai

// defaultMaxOutputTokens is the visible-answer budget Anthropic requires on every
// request; the extended-thinking budget is added on top of it.
const defaultMaxOutputTokens = 4096

// EffortConfig builds the provider-native generation config for one request,
// translating the reasoning effort and temperature into each backend's controls
// and gating them by the model's capabilities (resolved from the registry):
//
//   - effort is dropped for models that do not support reasoning;
//   - Anthropic reasoning uses the adaptive schema (thinking:{type:adaptive} +
//     output_config.effort) for adaptive models and the legacy enabled schema
//     (thinking:{type:enabled, budget_tokens}) otherwise;
//   - temperature is dropped for models that do not support it.
//
// It is the single source of truth for this translation, shared by the in-process
// genkit provider and clicky/aichat. Returns nil when there is nothing to send.
func EffortConfig(backend Backend, model string, effort Effort, maxTokens int, temperature *float64) map[string]any {
	caps := modelCapabilities(backend, model)
	cfg := map[string]any{}
	if temperature != nil && caps.Temperature {
		cfg["temperature"] = *temperature
	}
	switch backend {
	case BackendOpenAI, BackendCodexAgent, BackendCodexCLI, BackendCodexCmux:
		if caps.Reasoning && effort != EffortNone {
			cfg["reasoning_effort"] = openaiReasoningEffort(effort)
		}
	case BackendGemini, BackendGeminiCLI:
		if caps.Reasoning && effort != EffortNone {
			cfg["thinkingConfig"] = map[string]any{"thinkingBudget": thinkingBudget(effort)}
		}
	case BackendDeepSeek:
		// DeepSeek selects reasoning by model id (deepseek-reasoner vs deepseek-chat),
		// not a per-request effort knob, so there is no effort config to send.
	case BackendAnthropic, BackendClaudeAgent, BackendClaudeCLI, BackendClaudeCmux:
		// Anthropic requires max_tokens on every request. The thinking budget is
		// counted inside max_tokens, so reserve room for the visible answer on top
		// of it — but only when thinking is actually sent.
		base := maxTokens
		if base <= 0 {
			base = defaultMaxOutputTokens
		}
		if caps.Reasoning && effort != EffortNone {
			budget := thinkingBudget(effort)
			cfg["max_tokens"] = base + budget
			if caps.AdaptiveThinking {
				cfg["thinking"] = map[string]any{"type": "adaptive"}
				cfg["output_config"] = map[string]any{"effort": string(effort)}
			} else {
				cfg["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
			}
		} else {
			cfg["max_tokens"] = base
		}
	}
	if len(cfg) == 0 {
		return nil
	}
	return cfg
}

// modelCapabilities resolves a backend + model token to its registry entry so
// its capability flags can gate the generation config. Aliases and dated
// variants resolve to their canonical entry; an unresolved model reports no
// capabilities (zero value).
func modelCapabilities(backend Backend, model string) registryModel {
	provider := registryProviderForBackend(backend)
	if provider == "" {
		return registryModel{}
	}
	exact, ok := ResolveExactModelForBackend(backend, model)
	if !ok {
		return registryModel{}
	}
	m, _ := lookupRegistryExact(provider, exact)
	return m
}

// thinkingBudget maps a reasoning effort to an extended-thinking token budget
// (shared by Anthropic enabled-thinking and Gemini). xhigh is captain's top tier.
func thinkingBudget(e Effort) int {
	switch e {
	case EffortLow:
		return 2048
	case EffortMedium:
		return 8192
	case EffortHigh:
		return 24576
	case EffortXHigh, EffortMax, EffortUltra:
		return 32768
	default:
		return 0
	}
}

// openaiReasoningEffort passes Captain's model-validated OpenAI effort through
// unchanged. Unsupported model/tier combinations fail before provider execution.
func openaiReasoningEffort(e Effort) string {
	return string(e)
}
