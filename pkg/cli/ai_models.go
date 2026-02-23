package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/pricing"
)

type AIModelsOptions struct {
	Filter  string `flag:"filter" help:"Filter models by name substring" short:"f"`
	Backend string `flag:"backend" help:"Filter by backend (anthropic, gemini, codex-cli, claude-cli, gemini-cli)" short:"b"`
	Limit   int    `flag:"limit" help:"Maximum models to show" default:"50" short:"l"`
	All     bool   `flag:"all" help:"Include all OpenRouter models (not just built-in)" short:"a"`
}

type AIModelRow struct {
	Model     string `json:"model" pretty:"label=Model,width=45,table"`
	Backend   string `json:"backend" pretty:"label=Backend,table"`
	Input     string `json:"input" pretty:"label=Input/1M,table"`
	Output    string `json:"output" pretty:"label=Output/1M,table"`
	Context   string `json:"context" pretty:"label=Context,table"`
	MaxTokens string `json:"maxTokens" pretty:"label=Max Tokens,table"`
	Reasoning string `json:"reasoning" pretty:"label=Think,table"`
	Default   string `json:"default,omitempty" pretty:"label=Def,table"`
}

type AIModelsResult struct {
	Total int          `json:"total" pretty:"label=Total Models"`
	Rows  []AIModelRow `json:"rows"`
}

func RunAIModels(opts AIModelsOptions) (any, error) {
	// Ensure default models are registered
	_ = ai.DefaultModels()

	if opts.All {
		return runAllModels(opts)
	}
	return runDefaultModels(opts)
}

// runDefaultModels shows the built-in model catalog
func runDefaultModels(opts AIModelsOptions) (any, error) {
	defaults := ai.DefaultModels()

	filterLower := strings.ToLower(opts.Filter)
	backendFilter := ai.Backend(opts.Backend)

	rows := make([]AIModelRow, 0)
	for _, m := range defaults {
		if backendFilter != "" && m.Backend != backendFilter {
			continue
		}
		if opts.Filter != "" && !strings.Contains(strings.ToLower(m.ID), filterLower) && !strings.Contains(strings.ToLower(m.Name), filterLower) {
			continue
		}

		def := ""
		if m.Default {
			def = "✓"
		}
		reasoning := ""
		if m.Reasoning {
			reasoning = "✓"
		}

		rows = append(rows, AIModelRow{
			Model:     m.ID,
			Backend:   string(m.Backend),
			Input:     formatPrice(m.InputPrice),
			Output:    formatPrice(m.OutputPrice),
			Context:   formatContext(m.ContextWindow),
			MaxTokens: formatContext(m.MaxTokens),
			Reasoning: reasoning,
			Default:   def,
		})

		if opts.Limit > 0 && len(rows) >= opts.Limit {
			break
		}
	}

	return AIModelsResult{Total: len(rows), Rows: rows}, nil
}

// runAllModels shows all models from the pricing registry (OpenRouter + defaults)
func runAllModels(opts AIModelsOptions) (any, error) {
	models := pricing.ListModels(opts.Filter)

	// Build a set of default model IDs for marking
	defaultSet := make(map[string]ai.ModelDef)
	for _, m := range ai.DefaultModels() {
		defaultSet[m.ID] = m
	}

	rows := make([]AIModelRow, 0, min(len(models), opts.Limit))
	for _, m := range models {
		if len(rows) >= opts.Limit {
			break
		}

		def := ""
		reasoning := ""
		backend := ""

		if d, ok := defaultSet[m.ModelID]; ok {
			if d.Default {
				def = "✓"
			}
			if d.Reasoning {
				reasoning = "✓"
			}
			backend = string(d.Backend)
		}

		rows = append(rows, AIModelRow{
			Model:     m.ModelID,
			Backend:   backend,
			Input:     formatPrice(m.InputPrice),
			Output:    formatPrice(m.OutputPrice),
			Context:   formatContext(m.ContextWindow),
			MaxTokens: formatContext(m.MaxTokens),
			Reasoning: reasoning,
			Default:   def,
		})
	}

	return AIModelsResult{Total: len(models), Rows: rows}, nil
}

func formatPrice(price float64) string {
	if price == 0 {
		return "-"
	}
	if price < 0.01 {
		return fmt.Sprintf("$%.4f", price)
	}
	return fmt.Sprintf("$%.2f", price)
}

func formatContext(n int) string {
	if n == 0 {
		return "-"
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.0fK", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}
