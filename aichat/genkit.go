package aichat

import (
	"context"
	"fmt"
	"os"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/anthropic"
	"github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/firebase/genkit/go/plugins/googlegenai"
)

// initGenkit registers every provider plugin whose API key is present in the
// environment, defaulting the model to DefaultModelID. Plugins panic on Init
// when their key is missing, so an absent key means the provider is simply not
// registered (and selecting one of its models later fails loud in LookupModel +
// generation). Fails loud if no providers are configured at all.
func initGenkit(ctx context.Context) (*genkit.Genkit, error) {
	var plugins []api.Plugin

	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		plugins = append(plugins, &anthropic.Anthropic{})
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		plugins = append(plugins, &openai.OpenAI{APIKey: os.Getenv("OPENAI_API_KEY")})
	}
	if k := os.Getenv("GOOGLE_API_KEY"); k != "" {
		plugins = append(plugins, &googlegenai.GoogleAI{APIKey: k})
	} else if k := os.Getenv("GEMINI_API_KEY"); k != "" {
		plugins = append(plugins, &googlegenai.GoogleAI{APIKey: k})
	}

	if len(plugins) == 0 {
		return nil, fmt.Errorf("no AI provider configured: set ANTHROPIC_API_KEY, OPENAI_API_KEY, or GOOGLE_API_KEY/GEMINI_API_KEY")
	}

	g := genkit.Init(ctx,
		genkit.WithPlugins(plugins...),
		genkit.WithDefaultModel(DefaultModelID),
	)
	return g, nil
}

// effortConfig builds the provider-specific generation config that translates
// an Effort value into the provider's native reasoning control. Returns nil
// when there is nothing to set (no effort, or a non-reasoning model).
func effortConfig(m Model, e Effort) map[string]any {
	if !m.Reasoning || e == EffortNone {
		return nil
	}
	switch m.Provider {
	case ProviderOpenAI:
		// OpenAI o-series: reasoning_effort low|medium|high.
		return map[string]any{"reasoning_effort": string(e)}
	case ProviderGoogle:
		// Gemini 2.5+: thinkingConfig.thinkingBudget (token budget).
		return map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": geminiThinkingBudget(e)}}
	case ProviderAnthropic:
		// Anthropic: extended-thinking budget tokens.
		return map[string]any{"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": anthropicThinkingBudget(e),
		}}
	default:
		return nil
	}
}

func anthropicThinkingBudget(e Effort) int {
	switch e {
	case EffortLow:
		return 2048
	case EffortMedium:
		return 8192
	case EffortHigh:
		return 24576
	default:
		return 0
	}
}

func geminiThinkingBudget(e Effort) int {
	switch e {
	case EffortLow:
		return 2048
	case EffortMedium:
		return 8192
	case EffortHigh:
		return 24576
	default:
		return 0
	}
}

// generateOptions assembles the Generate options for a chat turn: model,
// messages, tools, system prompt, optional effort config, and the streaming
// callback.
func generateOptions(m Model, e Effort, system string, msgs []*ai.Message, tools []ai.ToolRef, stream ai.ModelStreamCallback) []ai.GenerateOption {
	opts := []ai.GenerateOption{
		ai.WithModelName(m.ID),
		ai.WithMessages(msgs...),
	}
	if system != "" {
		opts = append(opts, ai.WithSystem(system))
	}
	if len(tools) > 0 {
		opts = append(opts, ai.WithTools(tools...))
	}
	if cfg := effortConfig(m, e); cfg != nil {
		opts = append(opts, ai.WithConfig(cfg))
	}
	if stream != nil {
		opts = append(opts, ai.WithStreaming(stream))
	}
	return opts
}
