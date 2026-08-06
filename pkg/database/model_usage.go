package database

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	commonsmigrate "github.com/flanksource/commons-db/migrate"
)

type ModelUsageTotals struct {
	TotalTokens  int
	TotalCostUSD float64
}

// ModelUsageSince aggregates Captain model calls in the selected schemas.
// Configured schemas that have never been migrated have no usage and are
// skipped; a migrated schema missing Captain's ledger fails as corrupted.
func (db *DB) ModelUsageSince(ctx context.Context, since time.Time, schemas ...string) (ModelUsageTotals, error) {
	if db == nil || db.gorm == nil {
		return ModelUsageTotals{}, fmt.Errorf("captain database is not initialized")
	}
	if since.IsZero() {
		return ModelUsageTotals{}, fmt.Errorf("model usage start time is required")
	}
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
		usage, exists, err := db.modelUsageSince(ctx, since, schema)
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

func (db *DB) modelUsageSince(ctx context.Context, since time.Time, schema string) (ModelUsageTotals, bool, error) {
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
	var usage ModelUsageTotals
	err = db.gorm.WithContext(ctx).
		Table(schema+".captain_model_calls").
		Where("created_at >= ?", since).
		Select(`COALESCE(SUM(input_tokens + output_tokens), 0) AS total_tokens,
			COALESCE(SUM(input_cost + output_cost + reasoning_cost + cache_read_cost + cache_write_cost), 0) AS total_cost_usd`).
		Scan(&usage).Error
	if err != nil {
		return ModelUsageTotals{}, false, fmt.Errorf("aggregate Captain model usage for schema %q: %w", schema, err)
	}
	return usage, true, nil
}
