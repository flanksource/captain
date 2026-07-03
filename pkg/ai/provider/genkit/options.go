package genkit

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
)

// defaultMaxOutputTokens is the visible-answer budget Anthropic requires on every
// request; it is added on top of any extended-thinking budget.
const defaultMaxOutputTokens = 4096

// bareModel strips a leading provider prefix so a model id can be re-prefixed for
// either a genkit ref (anthropic/openai/googleai) or an OpenRouter pricing key.
func bareModel(model string) string {
	for _, prefix := range []string{"anthropic/", "openai/", "googleai/", "google/", "deepseek/", "models/"} {
		if strings.HasPrefix(model, prefix) {
			return strings.TrimPrefix(model, prefix)
		}
	}
	return model
}

// modelRef produces the genkit model reference for a backend+model
// (anthropic/<model>, openai/<model>, googleai/<model>).
func modelRef(backend ai.Backend, model string) (string, error) {
	if model == "" {
		return "", fmt.Errorf("genkit provider: model cannot be empty")
	}
	bare := bareModel(model)
	switch backend {
	case ai.BackendAnthropic:
		return "anthropic/" + bare, nil
	case ai.BackendOpenAI:
		return "openai/" + bare, nil
	case ai.BackendGemini:
		return "googleai/" + bare, nil
	case ai.BackendDeepSeek:
		return "deepseek/" + bare, nil
	default:
		return "", fmt.Errorf("genkit provider: unsupported backend %q", backend)
	}
}

// thinkingBudget maps a reasoning effort to an extended-thinking token budget
// (shared by Anthropic and Gemini). xhigh is captain's top tier.
func thinkingBudget(e api.Effort) int {
	switch e {
	case api.EffortLow:
		return 2048
	case api.EffortMedium:
		return 8192
	case api.EffortHigh:
		return 24576
	case api.EffortXHigh:
		return 32768
	default:
		return 0
	}
}

// openaiReasoningEffort maps captain's effort onto OpenAI's reasoning_effort
// values; OpenAI tops out at "high", so xhigh clamps to it.
func openaiReasoningEffort(e api.Effort) string {
	if e == api.EffortXHigh {
		return string(api.EffortHigh)
	}
	return string(e)
}

// anthropicMaxTokens returns max_tokens for an Anthropic request. The thinking
// budget is counted inside max_tokens, so leave room for the visible answer on
// top of it; honour an explicit budget MaxTokens as the visible-answer base.
func anthropicMaxTokens(req ai.Request, e api.Effort) int {
	base := req.Budget.MaxTokens
	if base <= 0 {
		base = defaultMaxOutputTokens
	}
	budget := thinkingBudget(e)
	if budget == 0 {
		return base
	}
	return budget + base
}

// effortConfig builds the provider-specific generation config translating the
// request's reasoning effort into each backend's native control. Anthropic also
// requires max_tokens on every request, so it always returns a config.
func effortConfig(backend ai.Backend, req ai.Request) map[string]any {
	e := req.Effort
	switch backend {
	case ai.BackendOpenAI:
		if e == api.EffortNone {
			return nil
		}
		return map[string]any{"reasoning_effort": openaiReasoningEffort(e)}
	case ai.BackendGemini:
		if e == api.EffortNone {
			return nil
		}
		return map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": thinkingBudget(e)}}
	case ai.BackendDeepSeek:
		// DeepSeek selects reasoning by model (deepseek-reasoner vs deepseek-chat),
		// not a per-request effort knob, so there is no effort config to send.
		return nil
	case ai.BackendAnthropic:
		cfg := map[string]any{"max_tokens": anthropicMaxTokens(req, e)}
		if e != api.EffortNone {
			cfg["thinking"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": thinkingBudget(e),
			}
		}
		return cfg
	default:
		return nil
	}
}

// generateOptions assembles the genkit Generate options for one turn: model,
// system prompt, user prompt, effort config, and (when streaming) the callback.
// WithOutputType is added only for the non-streaming structured-output path;
// ExecuteStream rejects structured output before calling this.
func generateOptions(p *Provider, req ai.Request, stream gkai.ModelStreamCallback) ([]gkai.GenerateOption, error) {
	opts := []gkai.GenerateOption{gkai.WithModelName(p.modelRef)}

	if req.Prompt.System != "" {
		opts = append(opts, gkai.WithSystem(req.Prompt.System))
	}
	opts = append(opts, gkai.WithPrompt(req.Prompt.User))

	if cfg := effortConfig(p.backend, req); cfg != nil {
		opts = append(opts, gkai.WithConfig(cfg))
	}
	if stream != nil {
		opts = append(opts, gkai.WithStreaming(stream))
	}
	if stream == nil {
		if len(req.Prompt.SchemaJSON) > 0 {
			var schema map[string]any
			if err := json.Unmarshal(req.Prompt.SchemaJSON, &schema); err != nil {
				return nil, fmt.Errorf("genkit %s: invalid Prompt.SchemaJSON: %w", p.backend, err)
			}
			opts = append(opts, gkai.WithOutputSchema(schema))
		} else if req.Prompt.Schema != nil {
			opts = append(opts, gkai.WithOutputType(req.Prompt.Schema))
		}
	}
	return opts, nil
}
