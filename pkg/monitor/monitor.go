// Package monitor is Captain's live session monitor: the single writer that
// keeps the native database current. It discovers claude/codex agent processes
// via ps and samples their CPU/RAM, tails the transcripts of live and
// captain-launched sessions with fsnotify (debounced), and incrementally
// backfills older transcripts by mtime/size. Every read surface (dashboard,
// CLI, API) reads the database this monitor writes.
package monitor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons/logger"
)

var log = logger.GetLogger("monitor")

// monitorLockNamespace/monitorLockKey ("CAPT", "MONI") serialize DB writers:
// exactly one monitor per database writes at a time; others idle or no-op.
const (
	monitorLockNamespace int32 = 0x43415054
	monitorLockKey       int32 = 0x4d4f4e49
)

type Config struct {
	DB     *database.DB
	HostID string
	// ProcessInterval is the ps poll cadence (default 5s).
	ProcessInterval time.Duration
	// Debounce delays transcript ingest after an fsnotify event (default 750ms).
	Debounce time.Duration
	// BackfillInterval is the periodic incremental scan cadence (default 5m).
	BackfillInterval time.Duration
	// DiscoverProcesses overrides ps-based agent-process discovery (tests).
	DiscoverProcesses func() ([]Process, error)
}

type Monitor struct {
	cfg Config
	db  *database.DB

	mu      sync.Mutex
	tracked map[string]string // transcript path -> source kind, the live tail set
}

func New(cfg Config) (*Monitor, error) {
	if cfg.DB == nil {
		return nil, errors.New("monitor requires a database")
	}
	if cfg.HostID == "" {
		cfg.HostID = "local"
	}
	if cfg.ProcessInterval <= 0 {
		cfg.ProcessInterval = 5 * time.Second
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 750 * time.Millisecond
	}
	if cfg.BackfillInterval <= 0 {
		cfg.BackfillInterval = 5 * time.Minute
	}
	if cfg.DiscoverProcesses == nil {
		cfg.DiscoverProcesses = discoverAgentProcesses
	}
	return &Monitor{cfg: cfg, db: cfg.DB, tracked: map[string]string{}}, nil
}

// TrackTranscript adds a transcript to the live tail set before its session is
// discoverable via ps — e.g. immediately after captain launches a prompt run.
func (m *Monitor) TrackTranscript(path, source string) {
	if path == "" {
		return
	}
	if source == "" {
		source = "claude"
	}
	m.mu.Lock()
	m.tracked[path] = source
	m.mu.Unlock()
}

func (m *Monitor) trackedPaths() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.tracked))
	for path, source := range m.tracked {
		out[path] = source
	}
	return out
}

func (m *Monitor) untrackTranscript(path string) {
	m.mu.Lock()
	delete(m.tracked, path)
	m.mu.Unlock()
}

// Run acquires the single-writer advisory lock and drives the three loops:
// process polling, fsnotify tailing, and periodic incremental backfill. When
// another monitor holds the lock, Run keeps retrying the lock instead of
// double-writing, so a serve restart takes over cleanly.
func (m *Monitor) Run(ctx context.Context) error {
	for {
		conn, acquired, err := m.tryAcquireWriterLock(ctx)
		if err != nil {
			return err
		}
		if acquired {
			err := m.runLocked(ctx, conn)
			_ = conn.Close()
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return err
			}
			continue
		}
		log.Infof("another Captain monitor holds the writer lock; standing by")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(m.cfg.ProcessInterval * 4):
		}
	}
}

func (m *Monitor) runLocked(ctx context.Context, lock *sql.Conn) error {
	ingestor := newIngestor(m)
	watcher, err := newTranscriptWatcher(m, ingestor)
	if err != nil {
		return err
	}
	defer watcher.close()

	// Initial backfill runs before the first process poll so live processes
	// bind to their ingested sessions instead of provisional stubs.
	m.backfill(ctx, ingestor)
	if err := m.pollProcesses(ctx, watcher); err != nil {
		log.Warnf("process poll: %v", err)
	}

	processTicker := time.NewTicker(m.cfg.ProcessInterval)
	backfillTicker := time.NewTicker(m.cfg.BackfillInterval)
	defer processTicker.Stop()
	defer backfillTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-processTicker.C:
			if err := m.pollProcesses(ctx, watcher); err != nil {
				log.Warnf("process poll: %v", err)
			}
		case <-backfillTicker.C:
			m.backfill(ctx, ingestor)
		case event, ok := <-watcher.events():
			if !ok {
				return errors.New("transcript watcher closed unexpectedly")
			}
			watcher.handle(ctx, event)
		case err, ok := <-watcher.errors():
			if ok && err != nil {
				log.Warnf("transcript watcher: %v", err)
			}
		}
	}
}

// tryAcquireWriterLock takes the monitor advisory lock on a dedicated
// connection so it is held for the monitor's lifetime, not a pooled statement.
func (m *Monitor) tryAcquireWriterLock(ctx context.Context) (*sql.Conn, bool, error) {
	sqlDB, err := m.db.Gorm().DB()
	if err != nil {
		return nil, false, fmt.Errorf("access Captain SQL pool: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("open Captain monitor lock connection: %w", err)
	}
	var acquired bool
	err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, $2)",
		monitorLockNamespace, monitorLockKey).Scan(&acquired)
	if err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("acquire Captain monitor lock: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	return conn, true, nil
}
