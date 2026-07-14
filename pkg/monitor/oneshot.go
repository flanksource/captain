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
	conn, _, err := m.tryAcquireWriterLock(ctx)
	if err != nil {
		return err
	}
	if conn == nil {
		return nil
	}
	defer func() { _ = conn.Close() }()

	ingestor := newIngestor(m)
	// Ingest transcripts before polling processes so processes bind to real
	// sessions instead of provisional stubs.
	m.backfill(ctx, ingestor)
	if err := m.pollProcesses(ctx, nil); err != nil {
		log.Warnf("one-shot process poll: %v", err)
	}
	return nil
}
