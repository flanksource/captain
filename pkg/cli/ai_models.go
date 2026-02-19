package cli

import (
	"fmt"

	"github.com/flanksource/captain/pkg/ai/pricing"
)

type AIModelsOptions struct {
	Filter string `flag:"filter" help:"Filter models by name substring" short:"f"`
	Limit  int    `flag:"limit" help:"Maximum models to show" default:"50" short:"l"`
}

type AIModelRow struct {
	Model     string `json:"model" pretty:"label=Model,table"`
	Input     string `json:"input" pretty:"label=Input/1M,table"`
	Output    string `json:"output" pretty:"label=Output/1M,table"`
	Context   string `json:"context" pretty:"label=Context,table"`
	MaxTokens string `json:"maxTokens" pretty:"label=Max Tokens,table"`
}

type AIModelsResult struct {
	Total int          `json:"total" pretty:"label=Total Models"`
	Rows  []AIModelRow `json:"rows"`
}

func RunAIModels(opts AIModelsOptions) (any, error) {
	models := pricing.ListModels(opts.Filter)

	rows := make([]AIModelRow, 0, min(len(models), opts.Limit))
	for i, m := range models {
		if i >= opts.Limit {
			break
		}
		rows = append(rows, AIModelRow{
			Model:     m.ModelID,
			Input:     formatPrice(m.InputPrice),
			Output:    formatPrice(m.OutputPrice),
			Context:   formatContext(m.ContextWindow),
			MaxTokens: formatContext(m.MaxTokens),
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
