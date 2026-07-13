package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
	commonsdb "github.com/flanksource/commons-db/db"
)

// captainDBState memoizes the process-wide native database handle. The
// database is mandatory: session/plan/prompt surfaces read it exclusively, so
// failing to open it is a loud error rather than a degraded mode.
var captainDBState struct {
	mu     sync.Mutex
	opened bool
	db     *database.DB
	err    error
}

// captainDB opens (once) the native Captain database: resolve the configured
// DSN (gavel-shared or explicit env) or start the shared embedded postgres,
// run migrations — including the idempotent legacy session-cache cutover — and
// wrap the pool.
func captainDB(ctx context.Context) (*database.DB, error) {
	captainDBState.mu.Lock()
	defer captainDBState.mu.Unlock()
	if !captainDBState.opened {
		captainDBState.db, captainDBState.err = openCaptainDB(ctx)
		captainDBState.opened = true
	}
	return captainDBState.db, captainDBState.err
}

// setCaptainDBForTest injects (or, with nil, resets) the process-wide handle
// so tests run against their own embedded database instead of a configured
// DSN. Production code never calls this.
func setCaptainDBForTest(db *database.DB) {
	captainDBState.mu.Lock()
	defer captainDBState.mu.Unlock()
	captainDBState.db = db
	captainDBState.err = nil
	captainDBState.opened = db != nil
}

func openCaptainDB(ctx context.Context) (*database.DB, error) {
	dsn, source, err := captainDSN()
	if err != nil {
		return nil, err
	}
	log.Debugf("captain database using %s", source)
	report, err := database.MigrateWithLegacySessionCutover(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate captain database (%s): %w", source, err)
	}
	if report != nil {
		log.Infof("migrated legacy session cache: %d sessions, %d prompt runs",
			report.ImportedSessionRows, report.ImportedPromptRunRows)
	}
	gormDB, err := commonsdb.NewGorm(dsn, commonsdb.DefaultGormConfig())
	if err != nil {
		return nil, fmt.Errorf("open captain database (%s): %w", source, err)
	}
	return database.Use(gormDB)
}

// serveMonitorState holds the serve process's live monitor so prompt-run code
// can register freshly launched transcripts for immediate tailing.
var serveMonitorState struct {
	mu  sync.RWMutex
	mon *monitor.Monitor
}

func setServeMonitor(mon *monitor.Monitor) {
	serveMonitorState.mu.Lock()
	serveMonitorState.mon = mon
	serveMonitorState.mu.Unlock()
}

func serveMonitor() *monitor.Monitor {
	serveMonitorState.mu.RLock()
	defer serveMonitorState.mu.RUnlock()
	return serveMonitorState.mon
}

func captainHostID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "local"
	}
	return host
}

// monitorDiscoverProcesses is indirected so cli tests can fake live-process
// discovery; nil selects the monitor's real ps-based discovery.
var monitorDiscoverProcesses func() ([]monitor.Process, error)

// freshenSessionDB runs a one-shot monitor pass (ps poll + incremental
// transcript scan) before a CLI read when no live monitor holds the writer
// lock. With serve running it is a fast no-op.
func freshenSessionDB(ctx context.Context) (*database.DB, error) {
	db, err := captainDB(ctx)
	if err != nil {
		return nil, err
	}
	config := monitor.Config{DB: db, HostID: captainHostID(), DiscoverProcesses: monitorDiscoverProcesses}
	if err := monitor.RunOnce(ctx, config); err != nil {
		return nil, fmt.Errorf("refresh session database: %w", err)
	}
	return db, nil
}

// captainDSN resolves the database connection: explicit env DSNs first, then a
// gavel-shared database, finally captain's own shared embedded postgres.
func captainDSN() (dsn, source string, err error) {
	for _, env := range []string{gavelDBEnvDSN, gavelCacheEnvDSN, captainSessionEnvDSN} {
		if dsn := strings.TrimSpace(os.Getenv(env)); dsn != "" {
			return dsn, env, nil
		}
	}
	dsn, source, err = gavelConfiguredSessionDSN()
	if err != nil {
		return "", "", fmt.Errorf("resolve gavel database: %w", err)
	}
	if dsn != "" {
		return dsn, source, nil
	}
	dir, err := sessionDBDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve captain database directory: %w", err)
	}
	// Shared embedded-postgres daemon: leave running for other captain processes.
	dsn, _, err = commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{DataDir: dir})
	if err != nil {
		return "", "", fmt.Errorf("start captain embedded database: %w", err)
	}
	return dsn, "captain embedded database", nil
}
