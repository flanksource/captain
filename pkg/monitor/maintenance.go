package monitor

import (
	"context"
	"os"
	"time"
)

// staleProcessCutoff is how long an open process row may go unobserved before
// the daily maintenance closes it. Live processes are re-sampled every poll,
// so only crash leftovers and rows from vanished monitors ever reach it.
const staleProcessCutoff = time.Hour

// maintain is the daily database upkeep that rides the recon backfill: prune
// ingest bookkeeping for transcripts deleted from disk, close process rows no
// poll has observed recently, and vacuum/analyze the embedded database.
func (m *Monitor) maintain(ctx context.Context) {
	sources, err := m.db.ListSessionSources(ctx)
	if err != nil {
		log.Warnf("maintenance: list transcript bookkeeping: %v", err)
		return
	}
	var missing []string
	for path := range sources {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, path)
		}
	}
	pruned, err := m.db.DeleteSessionSourcesByPaths(ctx, missing)
	if err != nil {
		log.Warnf("maintenance: prune transcript bookkeeping: %v", err)
	}
	closed, err := m.db.EndStaleSessionProcesses(ctx, time.Now().UTC().Add(-staleProcessCutoff))
	if err != nil {
		log.Warnf("maintenance: close stale processes: %v", err)
	}
	if err := m.db.VacuumAnalyze(ctx); err != nil {
		log.Warnf("maintenance: vacuum analyze: %v", err)
	}
	log.Infof("daily maintenance: pruned %d orphaned transcript sources, closed %d stale processes", pruned, closed)
}
