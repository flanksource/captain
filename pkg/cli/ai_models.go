package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/pricing"
)

type AIModelsOptions struct {
	Filter  string `flag:"filter" help:"Filter models by name substring" short:"f"`
	Backend string `flag:"backend" help:"Filter by backend: anthropic|gemini|openai|deepseek|claude-cli|claude-agent|claude-cmux|codex-cli|codex-agent|codex-cmux|gemini-cli" short:"b"`
	Limit   int    `flag:"limit" help:"Maximum models to show" default:"50" short:"l"`
	All     bool   `flag:"all" help:"Include all OpenRouter models" short:"a"`
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
	if opts.All {
		return runAllModels(opts)
	}
	return runLiveModels(opts)
}

// runLiveModels lists what the user's API credentials can actually call by
// hitting provider model endpoints, then augments each row with pricing and
// context-window data from the OpenRouter registry. There is no static fallback:
// if a backend has no configured credential or the call fails, the user learns
// about it directly instead of being shown a stale hard-coded catalog.
func runLiveModels(opts AIModelsOptions) (any, error) {
	backendFilter := ai.Backend(strings.TrimSpace(opts.Backend))
	if backendFilter != "" && !backendFilter.Valid() {
		return nil, fmt.Errorf("--backend must be one of: %s (got %q)", ai.BackendList(), opts.Backend)
	}
	// CLI/agent backends authenticate internally, so their models come from the
	// static catalog without an API key.
	if backendFilter != "" && backendFilter.Kind() == "cli" {
		return catalogModelsResult(opts, backendFilter), nil
	}

	ctx := context.Background()
	type fetched struct {
		backend ai.Backend
		models  []ai.ModelDef
		err     error
	}
	var results []fetched

	backends := []ai.Backend{ai.BackendOpenAI, ai.BackendAnthropic}
	if backendFilter != "" {
		backends = []ai.Backend{backendFilter}
	}
	for _, backend := range backends {
		models, err := ai.ListModels(ctx, backend)
		results = append(results, fetched{backend, models, err})
	}

	// Surface the first hard error. With no static fallback, an error means
	// the user has to fix their API key — silently swallowing it would
	// leave them staring at an empty list and guessing why.
	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("%s: %w", r.backend, r.err)
		}
	}

	filterLower := strings.ToLower(opts.Filter)
	rows := make([]AIModelRow, 0)
	for _, r := range results {
		for _, m := range r.models {
			if opts.Filter != "" && !strings.Contains(strings.ToLower(m.ID), filterLower) && !strings.Contains(strings.ToLower(m.Name), filterLower) {
				continue
			}
			// Hide legacy/non-chat IDs unless the user asked for them by
			// name via --filter. Filtering by user intent overrides the
			// blacklist so "ai models -f gpt-3.5" still works.
			if opts.Filter == "" && ai.IsLegacyModelIDForBackend(m.ID, r.backend) {
				continue
			}

			row := AIModelRow{
				Model:   m.ID,
				Backend: string(r.backend),
			}
			if info, ok := lookupPricing(r.backend, m.ID); ok {
				row.Input = formatPrice(info.InputPrice)
				row.Output = formatPrice(info.OutputPrice)
				row.Context = formatContext(info.ContextWindow)
				row.MaxTokens = formatContext(info.MaxTokens)
			} else {
				row.Input = "-"
				row.Output = "-"
				row.Context = "-"
				row.MaxTokens = "-"
			}
			rows = append(rows, row)
		}
	}

	// Stable, deterministic display order: backend first (so anthropic and
	// openai groups stay together when both are listed), then model id.
	// Sort happens before the limit cap so truncation is over the final
	// alphabetised list, not whichever provider's response came back first.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Backend != rows[j].Backend {
			return rows[i].Backend < rows[j].Backend
		}
		return rows[i].Model < rows[j].Model
	})

	if opts.Limit > 0 && len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
	}

	return AIModelsResult{Total: len(rows), Rows: rows}, nil
}

// catalogModelsResult lists a CLI/agent backend's models from the static
// catalog (no API key). --filter narrows by id/name substring; pricing and
// context columns are filled when the OpenRouter registry knows the model and
// shown as "-" otherwise. Rows arrive pre-sorted by id from agentCatalogModels.
func catalogModelsResult(opts AIModelsOptions, backend ai.Backend) AIModelsResult {
	filterLower := strings.ToLower(opts.Filter)
	rows := make([]AIModelRow, 0)
	for _, m := range agentCatalogModels(backend) {
		if opts.Filter != "" && !strings.Contains(strings.ToLower(m.ID), filterLower) && !strings.Contains(strings.ToLower(m.Name), filterLower) {
			continue
		}
		row := AIModelRow{Model: m.ID, Backend: string(backend), Input: "-", Output: "-", Context: "-", MaxTokens: "-"}
		if info, ok := lookupPricing(backend, m.ID); ok {
			row.Input = formatPrice(info.InputPrice)
			row.Output = formatPrice(info.OutputPrice)
			row.Context = formatContext(info.ContextWindow)
			row.MaxTokens = formatContext(info.MaxTokens)
		}
		rows = append(rows, row)
	}
	if opts.Limit > 0 && len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
	}
	return AIModelsResult{Total: len(rows), Rows: rows}
}

// lookupPricing uses the provider registry's canonical OpenRouter candidates so
// API and local-agent backends resolve the same model price.
func lookupPricing(backend ai.Backend, id string) (pricing.ModelInfo, bool) {
	for _, candidate := range ai.PricingIDs(backend, id) {
		if info, ok := pricing.GetModelInfo(candidate); ok {
			return info, true
		}
	}
	return pricing.ModelInfo{}, false
}

// runAllModels shows every model in the OpenRouter pricing registry. Pricing
// is the only source of truth; without the static catalog there is no
// `Default` or `Reasoning` flag to surface, so those columns are blank for
// rows in this listing.
func runAllModels(opts AIModelsOptions) (any, error) {
	models := pricing.ListModels(opts.Filter)

	rows := make([]AIModelRow, 0, min(len(models), opts.Limit))
	for _, m := range models {
		if len(rows) >= opts.Limit {
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
