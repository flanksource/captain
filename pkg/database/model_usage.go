package database

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	commonsmigrate "github.com/flanksource/commons-db/migrate"
	"gorm.io/gorm"
)

type ModelUsageTotals struct {
	TotalTokens  int
	TotalCostUSD float64
}

// ModelUsageQuery scopes an aggregation over the Captain model-call ledger.
type ModelUsageQuery struct {
	// Since bounds the window and is required: accumulated usage is always
	// measured against a period, never over all time.
	Since time.Time
	// Sessions is a subquery selecting the session IDs to count; nil counts
	// every session in the schema. A subquery rather than a materialised ID
	// list because only the embedding application knows who a session belongs
	// to, and it should not have to read a tenant's whole thread table to ask
	// Captain what that tenant spent.
	Sessions *gorm.DB
	// Schemas selects which Captain schemas to aggregate; empty means the
	// handle's own schema.
	Schemas []string
}

// modelUsageTotalsSelect mirrors api.Cost.Total() and the captain_session_costs
// view: prefer the provider-reported cost per call, with the list-price bucket
// sum as the fallback. Decided per row, not per group, so a mix of list-priced
// and provider-reported calls still totals. Non-USD calls contribute tokens but
// no cost — there is no rate table to convert them, and adding EUR to USD would
// be worse than reporting less.
const modelUsageTotalsSelect = `COALESCE(SUM(calls.input_tokens + calls.output_tokens + calls.reasoning_tokens + calls.cache_read_tokens + calls.cache_write_tokens), 0) AS total_tokens,
	COALESCE(SUM(CASE WHEN upper(calls.currency) = 'USD' THEN
		CASE WHEN calls.provider_cost_usd > 0 THEN calls.provider_cost_usd
		ELSE calls.input_cost + calls.output_cost + calls.reasoning_cost + calls.cache_read_cost + calls.cache_write_cost END
	ELSE 0 END), 0) AS total_cost_usd`

// ModelUsage aggregates Captain model calls in the selected schemas.
// Configured schemas that have never been migrated have no usage and are
// skipped; a migrated schema missing Captain's ledger fails as corrupted.
func (db *DB) ModelUsage(ctx context.Context, query ModelUsageQuery) (ModelUsageTotals, error) {
	if db == nil || db.gorm == nil {
		return ModelUsageTotals{}, fmt.Errorf("captain database is not initialized")
	}
	if query.Since.IsZero() {
		return ModelUsageTotals{}, fmt.Errorf("model usage start time is required")
	}
	schemas := query.Schemas
	if len(schemas) == 0 {
		schemas = []string{db.schema}
	}
	seen := map[string]struct{}{}
	var total ModelUsageTotals
	for _, schema := range schemas {
		schema = strings.TrimSpace(schema)
		if err := commonsmigrate.ValidateSchemaName(schema); err != nil {
			return ModelUsageTotals{}, fmt.Errorf("model usage schema: %w", err)
		}
		if _, duplicate := seen[schema]; duplicate {
			continue
		}
		seen[schema] = struct{}{}
		usage, exists, err := db.modelUsageInSchema(ctx, query, schema)
		if err != nil {
			return ModelUsageTotals{}, err
		}
		if !exists {
			continue
		}
		if usage.TotalTokens > math.MaxInt-total.TotalTokens {
			return ModelUsageTotals{}, fmt.Errorf("model usage token total overflows int")
		}
		total.TotalTokens += usage.TotalTokens
		total.TotalCostUSD += usage.TotalCostUSD
	}
	return total, nil
}

func (db *DB) modelUsageInSchema(ctx context.Context, query ModelUsageQuery, schema string) (ModelUsageTotals, bool, error) {
	var state struct {
		SchemaExists bool `gorm:"column:schema_exists"`
		LedgerExists bool `gorm:"column:ledger_exists"`
	}
	err := db.gorm.WithContext(ctx).Raw(`
		SELECT
			EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = ?) AS schema_exists,
			EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = ? AND table_name = 'captain_model_calls') AS ledger_exists`,
		schema, schema,
	).Scan(&state).Error
	if err != nil {
		return ModelUsageTotals{}, false, fmt.Errorf("inspect Captain usage schema %q: %w", schema, err)
	}
	if !state.SchemaExists {
		return ModelUsageTotals{}, false, nil
	}
	if !state.LedgerExists {
		return ModelUsageTotals{}, false, fmt.Errorf("captain usage schema %q is missing captain_model_calls", schema)
	}
	statement := db.gorm.WithContext(ctx).
		Table(schema+".captain_model_calls AS calls").
		Where("calls.created_at >= ?", query.Since)
	if query.Sessions != nil {
		statement = statement.
			Joins("JOIN "+schema+".captain_turns AS turns ON turns.id = calls.turn_id").
			Where("turns.session_id IN (?)", query.Sessions)
	}
	var usage ModelUsageTotals
	if err := statement.Select(modelUsageTotalsSelect).Scan(&usage).Error; err != nil {
		return ModelUsageTotals{}, false, fmt.Errorf("aggregate Captain model usage for schema %q: %w", schema, err)
	}
	return usage, true, nil
}
