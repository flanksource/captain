package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/pricing"
)

// legacyModelPrefixes hides model IDs that are either superseded by a newer
// generation (so almost no one should be picking them) or aren't chat
// completions at all (image/audio/embedding/moderation endpoints). Used by
// both the `ai models` listing and the `configure` wizard's model picker so
// the two views stay consistent.
//
// `--filter` (or any explicit substring filter) overrides the blacklist —
// "ai models -f gpt-3.5" or a user typing "grok-3" into the picker will
// still see those entries.
var legacyModelPrefixes = []string{
	// OpenAI legacy
	"gpt-3",
	"gpt-4",  // covers gpt-4, gpt-4o, gpt-4.1, gpt-4-turbo, ...
	"gpt-5-", // hides every gpt-5 variant (mini, nano, codex, pro, ...) but keeps the bare "gpt-5", "gpt-5.1", "gpt-5.2", "gpt-5.3"
	"o1",
	"o3-",
	"codex-mini",
	// OpenAI non-chat endpoints
	"dall-",
	"whisper",
	"tts-",
	"text-embedding",
	"text-moderation",
	"omni-moderation",
	"babbage",
	"davinci",
	"chatgpt-",
	"computer-use-preview",
	// Claude legacy (3.x and earlier 4-line latests/dated)
	"claude-3",
	"claude-2",
	"claude-instant",
	"claude-sonnet-4-0",
	"claude-sonnet-4-2",
	"claude-opus-4-0",
	"claude-opus-4-1",
	// Gemini legacy
	"gemini-1",
	"gemini-2.0",
	// Grok legacy (3-line is two generations behind grok-4)
	"grok-3",
	"grok-code-fast-1",
}

func isLegacyModelID(id string) bool {
	idLower := strings.ToLower(id)
	for _, p := range legacyModelPrefixes {
		if strings.HasPrefix(idLower, p) {
			return true
		}
	}
	return false
}

type AIModelsOptions struct {
	Filter  string `flag:"filter" help:"Filter models by name substring" short:"f"`
	Backend string `flag:"backend" help:"Filter by backend (anthropic, openai)" short:"b"`
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

// runLiveModels lists what the user's API keys can actually call by hitting
// OpenAI and Anthropic /v1/models, then augments each row with pricing and
// context-window data from the OpenRouter registry. There is no static
// fallback: if a backend has no API key set or the call fails, the user
// learns about it directly so they can fix their environment instead of
// being shown a stale hard-coded catalog.
func runLiveModels(opts AIModelsOptions) (any, error) {
	backendFilter := ai.Backend(strings.TrimSpace(opts.Backend))
	switch backendFilter {
	case "", ai.BackendOpenAI, ai.BackendAnthropic:
	default:
		return nil, fmt.Errorf("--backend must be one of: openai, anthropic (got %q)", opts.Backend)
	}

	ctx := context.Background()
	type fetched struct {
		backend ai.Backend
		models  []ai.ModelDef
		err     error
	}
	var results []fetched

	if backendFilter == "" || backendFilter == ai.BackendOpenAI {
		m, err := ai.FetchOpenAIModels(ctx, openAIAPIKey())
		results = append(results, fetched{ai.BackendOpenAI, m, err})
	}
	if backendFilter == "" || backendFilter == ai.BackendAnthropic {
		m, err := ai.FetchAnthropicModels(ctx, anthropicAPIKey())
		results = append(results, fetched{ai.BackendAnthropic, m, err})
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
			if opts.Filter == "" && isLegacyModelID(m.ID) {
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

func openAIAPIKey() string    { return strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) }
func anthropicAPIKey() string { return strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) }

// lookupPricing tries the model id as-is first, then with the OpenRouter
// "provider/model" prefix that the registry actually uses for the major
// providers (OpenAI's `gpt-5` is keyed as `openai/gpt-5` upstream).
func lookupPricing(backend ai.Backend, id string) (pricing.ModelInfo, bool) {
	if info, ok := pricing.GetModelInfo(id); ok {
		return info, true
	}
	prefix := openRouterPrefix(backend)
	if prefix == "" {
		return pricing.ModelInfo{}, false
	}
	if info, ok := pricing.GetModelInfo(prefix + "/" + id); ok {
		return info, true
	}
	return pricing.ModelInfo{}, false
}

func openRouterPrefix(backend ai.Backend) string {
	switch backend {
	case ai.BackendOpenAI:
		return "openai"
	case ai.BackendAnthropic:
		return "anthropic"
	}
	return ""
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
