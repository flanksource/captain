package database

import (
	"context"
	"fmt"
)

// PromptRunOverviewChannel is notified with a prompt run's id whenever its
// captain_prompt_run_overview row changes. See 89_prompt_run_overview_rows.sql.
const PromptRunOverviewChannel = "captain_prompt_run_overview_change"

// EnsurePromptRunOverview rebuilds the captain_prompt_run_overview_rows a crash
// truncated: the table is UNLOGGED, so it comes back empty while prompt runs
// still exist. It rebuilds every run without a row and reports whether there
// was one. Writes before it runs recompute the rows of the runs they touch, so
// it completes the rest rather than gating writers.
func (db *DB) EnsurePromptRunOverview(ctx context.Context) (bool, error) {
	if err := db.requireGorm(); err != nil {
		return false, err
	}
	var rebuilt bool
	if err := db.gorm.WithContext(ctx).Raw("SELECT captain_prompt_run_overview_ensure()").Scan(&rebuilt).Error; err != nil {
		return false, fmt.Errorf("ensure Captain prompt-run overview rows: %w", err)
	}
	return rebuilt, nil
}
