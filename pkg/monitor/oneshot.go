package monitor

import (
	"context"
)

// RunOnce freshens the database exactly once: one process poll and one
// incremental backfill pass, guarded by the same single-writer advisory lock as
// Run. When a live monitor already holds the lock the database is being kept
// current continuously and RunOnce is a no-op.
func RunOnce(ctx context.Context, cfg Config) error {
	m, err := New(cfg)
	if err != nil {
		return err
	}
	conn, acquired, err := m.tryAcquireWriterLock(ctx)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer func() { _ = conn.Close() }()

	ingestor := newIngestor(m)
	if err := ingestor.refreshSourceStates(ctx); err != nil {
		return err
	}
	if err := m.pollProcesses(ctx, nil); err != nil {
		log.Warnf("one-shot process poll: %v", err)
	}
	m.backfill(ctx, ingestor)
	return nil
}
