package database

import (
	"context"
	"fmt"
)

// PromptRunOverviewChannel is notified with a prompt run's id whenever its
// captain_prompt_run_overview row changes. See 89_prompt_run_overview_rows.sql.
const PromptRunOverviewChannel = "captain_prompt_run_overview_change"

// promptRunOverviewEnsureBatch bounds how many runs one ensure transaction
// rebuilds, so a post-crash backfill never holds every overview row lock at
// once while writers keep committing.
const promptRunOverviewEnsureBatch = 500

// EnsurePromptRunOverview rebuilds the captain_prompt_run_overview_rows a crash
// truncated: the table is UNLOGGED, so it comes back empty while prompt runs
// still exist. It rebuilds every run without a row, in batches, and reports
// whether there was one. Writes before it runs recompute the rows of the runs
// they touch, so it completes the rest rather than gating writers.
func (db *DB) EnsurePromptRunOverview(ctx context.Context) (bool, error) {
	if err := db.requireGorm(); err != nil {
		return false, err
	}
	rebuiltAny := false
	for {
		var rebuilt bool
		if err := db.gorm.WithContext(ctx).
			Raw(`SELECT captain_prompt_run_overview_ensure(?)`, promptRunOverviewEnsureBatch).
			Scan(&rebuilt).Error; err != nil {
			return rebuiltAny, fmt.Errorf("ensure Captain prompt-run overview rows: %w", err)
		}
		if !rebuilt {
			return rebuiltAny, nil
		}
		rebuiltAny = true
	}
}
