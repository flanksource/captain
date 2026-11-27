package runowner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"
)

// Reclaim cancels runID when nothing drives it any more, as
// database.ReclaimPromptRun does with Judge, and logs what it reclaimed. It
// returns nil when the run is still driven, waiting, or already settled.
func Reclaim(ctx context.Context, db *database.DB, runID uuid.UUID) (*database.PromptRun, error) {
	run, err := db.ReclaimPromptRun(ctx, runID, Judge(time.Now()))
	if err != nil {
		return nil, err
	}
	if run != nil {
		logger.Warnf("runowner: reclaimed orphaned prompt run %s on session %s: %s", run.ID, run.SessionID, run.Error)
	}
	return run, nil
}

// ReclaimOrphans reclaims every pending or running run whose driver is gone,
// and reports how many it reclaimed. Each run is judged again under its row
// lock before it is cancelled; the unlocked first pass only spares the runs
// that are plainly driven from being locked at all.
func ReclaimOrphans(ctx context.Context, db *database.DB) (int, error) {
	runs, err := db.ListDrivenPromptRuns(ctx)
	if err != nil {
		return 0, err
	}
	judge := Judge(time.Now())
	reclaimed := 0
	var failures []error
	for _, run := range runs {
		if alive, _ := judge(run); alive {
			continue
		}
		settled, err := Reclaim(ctx, db, run.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("reclaim prompt run %s: %w", run.ID, err))
			continue
		}
		if settled != nil {
			reclaimed++
		}
	}
	return reclaimed, errors.Join(failures...)
}
