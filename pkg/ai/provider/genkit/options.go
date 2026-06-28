package genkit

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"

	gkai "github.com/firebase/genkit/go/ai"
)

// defaultMaxOutputTokens is the visible-answer budget Anthropic requires on every
// request; it is added on top of any extended-thinking budget.
const defaultMaxOutputTokens = 4096

// effort is the normalized reasoning level parsed from ai.Request.ReasoningEffort.
type effort string

const (
	effortNone   effort = ""
	effortLow    effort = "low"
	effortMedium effort = "medium"
	effortHigh   effort = "high"
)

func parseEffort(s string) effort {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return effortLow
	case "medium":
		return effortMedium
	case "high":
		return effortHigh
	default:
		return effortNone
	}
}

// bareModel strips a leading provider prefix so a model id can be re-prefixed for
// either a genkit ref (anthropic/openai/googleai) or an OpenRouter pricing key.
func bareModel(model string) string {
	for _, prefix := range []string{"anthropic/", "openai/", "googleai/", "google/", "models/"} {
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
	default:
		return "", fmt.Errorf("genkit provider: unsupported backend %q", backend)
	}
}

func anthropicThinkingBudget(e effort) int {
	switch e {
	case effortLow:
		return 2048
	case effortMedium:
		return 8192
	case effortHigh:
		return 24576
	default:
		return 0
	}
}

func geminiThinkingBudget(e effort) int {
	switch e {
	case effortLow:
		return 2048
	case effortMedium:
		return 8192
	case effortHigh:
		return 24576
	default:
		return 0
	}
}

// anthropicMaxTokens returns max_tokens for an Anthropic request. The thinking
// budget is counted inside max_tokens, so leave room for the visible answer on
// top of it; honour an explicit ai.Request.MaxTokens as the visible-answer base.
func anthropicMaxTokens(req ai.Request, e effort) int {
	base := req.MaxTokens
	if base <= 0 {
		base = defaultMaxOutputTokens
	}
	budget := anthropicThinkingBudget(e)
	if budget == 0 {
		return base
	}
	return budget + base
}

// effortConfig builds the provider-specific generation config translating the
// request's reasoning effort into each backend's native control. Anthropic also
// requires max_tokens on every request, so it always returns a config.
func effortConfig(backend ai.Backend, req ai.Request) map[string]any {
	e := parseEffort(req.ReasoningEffort)
	switch backend {
	case ai.BackendOpenAI:
		if e == effortNone {
			return nil
		}
		return map[string]any{"reasoning_effort": string(e)}
	case ai.BackendGemini:
		if e == effortNone {
			return nil
		}
		return map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": geminiThinkingBudget(e)}}
	case ai.BackendAnthropic:
		cfg := map[string]any{"max_tokens": anthropicMaxTokens(req, e)}
		if e != effortNone {
			cfg["thinking"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": anthropicThinkingBudget(e),
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
func generateOptions(p *Provider, req ai.Request, stream gkai.ModelStreamCallback) []gkai.GenerateOption {
	opts := []gkai.GenerateOption{gkai.WithModelName(p.modelRef)}

	if req.SystemPrompt != "" {
		opts = append(opts, gkai.WithSystem(req.SystemPrompt))
	}
	opts = append(opts, gkai.WithPrompt(req.Prompt))

	if cfg := effortConfig(p.backend, req); cfg != nil {
		opts = append(opts, gkai.WithConfig(cfg))
	}
	if stream != nil {
		opts = append(opts, gkai.WithStreaming(stream))
	}
	if req.StructuredOutput != nil && stream == nil {
		opts = append(opts, gkai.WithOutputType(req.StructuredOutput))
	}
	return opts
}
