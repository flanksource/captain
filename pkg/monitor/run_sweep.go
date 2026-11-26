package monitor

import (
	"context"

	"github.com/flanksource/captain/pkg/runowner"
)

// reclaimOrphanedRuns cancels the pending and running prompt runs whose driver
// is gone. Admission reclaims the run holding a session it needs; this sweep
// reclaims the rest, so a dead host's runs stop reading as in flight on every
// dashboard. It runs ahead of the approval sweep, whose verdicts read run state.
func (m *Monitor) reclaimOrphanedRuns(ctx context.Context) {
	reclaimed, err := runowner.ReclaimOrphans(ctx, m.db)
	if err != nil {
		log.Warnf("orphaned prompt run sweep: %v", err)
	}
	if reclaimed > 0 {
		log.Infof("orphaned prompt run sweep: reclaimed %d run(s) whose driver is gone", reclaimed)
	}
}
