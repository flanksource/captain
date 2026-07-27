package registry

// defaultMaxOutputTokens is the visible-answer budget Anthropic requires on every
// request; the extended-thinking budget is added on top of it.
const defaultMaxOutputTokens = 4096

// GenerationConfig builds the provider-native generation config for one request,
// translating reasoning effort and temperature into this provider's controls and
// gating them by the model's catalog capabilities:
//
//   - effort is dropped for models that do not support reasoning;
//   - temperature is dropped for models that do not support it;
//   - Anthropic reasoning uses the adaptive schema (thinking:{type:adaptive} +
//     output_config.effort) for adaptive models and the legacy enabled schema
//     (thinking:{type:enabled, budget_tokens}) otherwise.
//
// Returns nil when there is nothing to send.
func (p *Provider) GenerationConfig(mode RuntimeMode, model string, effort Effort, maxTokens int, temperature *float64) map[string]any {
	caps := p.modelCaps(mode, model)
	cfg := map[string]any{}
	if temperature != nil && caps.Temperature {
		cfg["temperature"] = *temperature
	}
	if p.genConfig != nil {
		p.genConfig(cfg, caps, effort, maxTokens)
	}
	if len(cfg) == 0 {
		return nil
	}
	return cfg
}

// modelCaps resolves a model token to its catalog row so its capability flags can
// gate the generation config. An unresolved model reports no capabilities.
func (p *Provider) modelCaps(mode RuntimeMode, model string) KnownModel {
	exact, ok := p.ResolveExact(mode, model)
	if !ok {
		return KnownModel{}
	}
	m, _ := p.lookupExact(exact)
	return m
}

// openaiGenerationConfig sends the reasoning tier as-is; captain's effort enum is
// validated against the model before execution.
func openaiGenerationConfig(cfg map[string]any, caps KnownModel, effort Effort, _ int) {
	if caps.Reasoning && effort != EffortNone {
		cfg["reasoning_effort"] = string(effort)
	}
}

func googleGenerationConfig(cfg map[string]any, caps KnownModel, effort Effort, _ int) {
	if caps.Reasoning && effort != EffortNone {
		cfg["thinkingConfig"] = map[string]any{"thinkingBudget": thinkingBudget(effort)}
	}
}

// anthropicGenerationConfig always sends max_tokens, which Anthropic requires.
// The thinking budget counts inside max_tokens, so room for the visible answer is
// reserved on top of it — but only when thinking is actually sent.
func anthropicGenerationConfig(cfg map[string]any, caps KnownModel, effort Effort, maxTokens int) {
	base := maxTokens
	if base <= 0 {
		base = defaultMaxOutputTokens
	}
	if !caps.Reasoning || effort == EffortNone {
		cfg["max_tokens"] = base
		return
	}
	budget := thinkingBudget(effort)
	cfg["max_tokens"] = base + budget
	if caps.AdaptiveThinking {
		cfg["thinking"] = map[string]any{"type": "adaptive"}
		cfg["output_config"] = map[string]any{"effort": string(effort)}
		return
	}
	cfg["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
}

// DeepSeek selects reasoning by model id (deepseek-reasoner vs deepseek-chat)
// rather than a per-request knob, so it contributes no effort config — hence no
// genConfig hook on that descriptor.

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
