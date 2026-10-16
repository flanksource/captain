package database

import (
	"context"

	"github.com/flanksource/commons/logger"
)

// EnsureOverviews completes the UNLOGGED overview tables (migrations 88 and
// 89) after crash recovery truncated them. Writes recompute the rows they
// touch, so this fills in only the sessions and runs nothing has written to
// since; it runs where Captain starts writing, on Open with migrations and when
// a monitor takes the writer lock.
func (db *DB) EnsureOverviews(ctx context.Context) error {
	sessions, err := db.EnsureSessionOverview(ctx)
	if err != nil {
		return err
	}
	runs, err := db.EnsurePromptRunOverview(ctx)
	if err != nil {
		return err
	}
	if sessions || runs {
		logger.Infof("captain: rebuilt overview rows missing after crash recovery (sessions=%t, prompt runs=%t)", sessions, runs)
	}
	return nil
}
