// Package monitor is Captain's live session monitor: the single writer that
// keeps the native database current. Provider hooks (Claude Code lifecycle
// hooks, codex notify) are the primary real-time signal: they push exact
// session identity and transcript location the moment a session starts, makes
// progress, or ends. fsnotify tails live transcripts between hook events
// (debounced). Polling is demoted to two jobs: an adaptive ps poll that
// samples CPU/RAM and reaps vanished processes (crashes never emit
// SessionEnd), and a daily recon that backfills anything hooks missed plus
// database maintenance. Every read surface (dashboard, CLI, API) reads the
// database this monitor writes.
package monitor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	monitorLockAppPrefix       = "captain-monitor:"
)

type Config struct {
	DB     *database.DB
	HostID string
	// ProcessInterval is the ps poll cadence while sessions are active
	// (default 5s): agent processes were seen or a hook event arrived within
	// activityWindow.
	ProcessInterval time.Duration
	// IdleProcessInterval is the relaxed ps poll cadence when no session
	// activity has been observed recently (default 60s) — the poll then only
	// serves as the stale-process reaper and hookless-session fallback.
	IdleProcessInterval time.Duration
	// Debounce delays transcript ingest after an fsnotify event (default 750ms).
	Debounce time.Duration
	// BackfillInterval is the recon cadence (default 24h): a full incremental
	// scan over every known transcript plus database maintenance, catching
	// whatever hooks and fsnotify missed.
	BackfillInterval time.Duration
	// DiscoverProcesses overrides ps-based agent-process discovery (tests).
	DiscoverProcesses func() ([]Process, error)
}

// activityWindow is how long after the last observed session activity (agent
// process seen, hook event received) the process poll stays on the fast cadence.
const activityWindow = 2 * time.Minute

type Monitor struct {
	cfg Config
	db  *database.DB

	mu      sync.Mutex
	tracked map[string]string // transcript path -> source kind, the live tail set

	hookEvents chan HookEvent

	activityMu   sync.Mutex
	lastActivity time.Time

	maintenanceDue atomic.Bool

	// Monitor-lifetime, not ingestor-lifetime: an ingestor is built per run and
	// per hook batch, and the counters have to outlive all of them.
	ingest ingestMetrics

	readyOnce sync.Once
	ready     chan struct{}
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
	if cfg.IdleProcessInterval <= 0 {
		cfg.IdleProcessInterval = 60 * time.Second
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 750 * time.Millisecond
	}
	if cfg.BackfillInterval <= 0 {
		cfg.BackfillInterval = 24 * time.Hour
	}
	if cfg.DiscoverProcesses == nil {
		cfg.DiscoverProcesses = discoverAgentProcesses
	}
	return &Monitor{
		cfg: cfg, db: cfg.DB, tracked: map[string]string{},
		hookEvents: make(chan HookEvent, 128),
		ready:      make(chan struct{}),
	}, nil
}

// noteActivity records session activity: an agent process observed by the
// poll or a hook event. It keeps the process poll on the fast cadence.
func (m *Monitor) noteActivity(at time.Time) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	m.activityMu.Lock()
	if at.After(m.lastActivity) {
		m.lastActivity = at
	}
	m.activityMu.Unlock()
}

func (m *Monitor) idle() bool {
	m.activityMu.Lock()
	defer m.activityMu.Unlock()
	return time.Since(m.lastActivity) > activityWindow
}

// nextPollInterval picks the process poll cadence: fast while session
// activity was observed within activityWindow, relaxed otherwise.
func (m *Monitor) nextPollInterval() time.Duration {
	if m.idle() {
		return m.cfg.IdleProcessInterval
	}
	return m.cfg.ProcessInterval
}

// Ready is closed after the first process reconciliation attempt, or as soon
// as Run confirms that another monitor already owns the writer lock. Callers
// can use it as a lightweight startup barrier without waiting for historical
// backfill.
func (m *Monitor) Ready() <-chan struct{} { return m.ready }

func (m *Monitor) markReady() {
	if m == nil || m.ready == nil {
		return
	}
	m.readyOnce.Do(func() { close(m.ready) })
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
	defer m.markReady()
	for {
		conn, holderPID, err := m.tryAcquireWriterLock(ctx)
		if err != nil {
			return err
		}
		if conn != nil {
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
		m.markReady()
		log.Infof("another Captain monitor (PID %d) holds the writer lock; standing by", holderPID)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(m.cfg.ProcessInterval * 4):
		}
	}
}

func (m *Monitor) runLocked(ctx context.Context, lock *sql.Conn) error {
	runCtx, cancel := context.WithCancel(ctx)
	ingestor := newIngestor(m)
	watcher, err := newTranscriptWatcher(m, ingestor)
	if err != nil {
		cancel()
		return err
	}
	defer watcher.close()

	// Arm live transcript directories before the historical scan. Backfill can
	// take minutes on a cold database; it must never prevent fsnotify events or
	// process polling from keeping a newly launched session current.
	if err := m.pollProcesses(runCtx, watcher); err != nil {
		log.Warnf("process poll: %v", err)
	}
	m.markReady()

	backfillRequests := make(chan struct{}, 1)
	var backfillWG sync.WaitGroup
	backfillWG.Add(1)
	go func() {
		defer backfillWG.Done()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-backfillRequests:
				m.backfill(runCtx, ingestor)
				if m.maintenanceDue.CompareAndSwap(true, false) {
					m.maintain(runCtx)
				}
			}
		}
	}()
	defer func() {
		cancel()
		backfillWG.Wait()
	}()
	requestBackfill(backfillRequests)

	processTicker := time.NewTicker(m.nextPollInterval())
	backfillTicker := time.NewTicker(m.cfg.BackfillInterval)
	defer processTicker.Stop()
	defer backfillTicker.Stop()

	for {
		select {
		case <-runCtx.Done():
			return nil
		case <-processTicker.C:
			if err := m.pollProcesses(runCtx, watcher); err != nil {
				log.Warnf("process poll: %v", err)
			}
			processTicker.Reset(m.nextPollInterval())
		case ev := <-m.hookEvents:
			// A hook after an idle stretch snaps the poll back to the fast
			// cadence; during sustained activity the ticker is left alone so
			// frequent events cannot starve the reaper.
			wasIdle := m.idle()
			m.handleHookEvent(runCtx, watcher, ingestor, ev)
			if wasIdle {
				processTicker.Reset(m.cfg.ProcessInterval)
			}
		case <-backfillTicker.C:
			m.maintenanceDue.Store(true)
			requestBackfill(backfillRequests)
		case event, ok := <-watcher.events():
			if !ok {
				return errors.New("transcript watcher closed unexpectedly")
			}
			watcher.handle(runCtx, event)
		case err, ok := <-watcher.errors():
			if ok && err != nil {
				log.Warnf("transcript watcher: %v", err)
			}
		}
	}
}

// requestBackfill coalesces scan requests while one scan is already running.
// The worker remains the sole backfill caller, while the monitor event loop is
// free to service live transcript writes and process polls.
func requestBackfill(requests chan<- struct{}) {
	select {
	case requests <- struct{}{}:
	default:
	}
}

// tryAcquireWriterLock takes the monitor advisory lock on a dedicated
// connection so it is held for the monitor's lifetime, not a pooled statement.
func (m *Monitor) tryAcquireWriterLock(ctx context.Context) (*sql.Conn, int, error) {
	sqlDB, err := m.db.Gorm().DB()
	if err != nil {
		return nil, 0, fmt.Errorf("access Captain SQL pool: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("open Captain monitor lock connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT set_config('application_name', $1, false)",
		fmt.Sprintf("%s%d", monitorLockAppPrefix, os.Getpid())); err != nil {
		_ = conn.Close()
		return nil, 0, fmt.Errorf("register Captain monitor lock owner: %w", err)
	}
	for {
		var acquired bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, $2)",
			monitorLockNamespace, monitorLockKey).Scan(&acquired); err != nil {
			_ = conn.Close()
			return nil, 0, fmt.Errorf("acquire Captain monitor lock: %w", err)
		}
		if acquired {
			return conn, 0, nil
		}

		holderPID, err := queryMonitorLockHolderPID(ctx, conn)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		_ = conn.Close()
		if err != nil {
			return nil, 0, err
		}
		return nil, holderPID, nil
	}
}

func queryMonitorLockHolderPID(ctx context.Context, conn *sql.Conn) (int, error) {
	var applicationName string
	err := conn.QueryRowContext(ctx, `
		SELECT activity.application_name
		FROM pg_catalog.pg_locks AS lock
		JOIN pg_catalog.pg_stat_activity AS activity ON activity.pid = lock.pid
		WHERE lock.locktype = 'advisory'
		  AND lock.database = (
			SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database()
		  )
		  AND lock.classid = $1::oid
		  AND lock.objid = $2::oid
		  AND lock.objsubid = 2
		  AND lock.granted
		LIMIT 1`, monitorLockNamespace, monitorLockKey).Scan(&applicationName)
	if err != nil {
		return 0, fmt.Errorf("identify Captain monitor lock owner: %w", err)
	}
	value, ok := strings.CutPrefix(applicationName, monitorLockAppPrefix)
	if !ok {
		return 0, fmt.Errorf("captain monitor lock owner has unexpected application name %q", applicationName)
	}
	holderPID, err := strconv.Atoi(value)
	if err != nil || holderPID <= 0 {
		return 0, fmt.Errorf("captain monitor lock owner has invalid PID in application name %q", applicationName)
	}
	return holderPID, nil
}
