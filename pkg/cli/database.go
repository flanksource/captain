package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/spf13/pflag"
	"gorm.io/gorm"
)

const databaseURLFlag = "db-url"

var databaseURL string

// BindDatabaseURLFlag exposes the process database as a root persistent flag.
// The explicit CLI value wins over environment variables and config files.
func BindDatabaseURLFlag(flags *pflag.FlagSet) {
	flags.StringVar(&databaseURL, databaseURLFlag, "", "PostgreSQL database URL (overrides environment and db.json)")
}

// captainDBState memoizes the process-wide native database handle. The
// database is mandatory: session/plan/prompt surfaces read it exclusively, so
// failing to open it is a loud error rather than a degraded mode.
var captainDBState struct {
	mu       sync.Mutex
	opened   bool
	migrated bool
	db       *database.DB
	dsn      string
	source   string
	err      error
}

type captainDatabaseMode uint8

const (
	captainDatabaseNoMigrations captainDatabaseMode = iota
	captainDatabaseWithMigrations
)

// captainDB opens the native Captain database without running migrations.
func captainDB(ctx context.Context) (*database.DB, error) {
	return captainDBForMode(ctx, captainDatabaseNoMigrations)
}

func captainServeDB(ctx context.Context) (*database.DB, error) {
	return captainDBForMode(ctx, captainDatabaseWithMigrations)
}

func captainDBForMode(ctx context.Context, mode captainDatabaseMode) (*database.DB, error) {
	captainDBState.mu.Lock()
	defer captainDBState.mu.Unlock()
	if captainDBState.opened {
		if mode == captainDatabaseWithMigrations && !captainDBState.migrated {
			return nil, errors.New("captain serve cannot migrate after the process database was opened without migrations")
		}
		return captainDBState.db, captainDBState.err
	}
	captainDBState.db, captainDBState.dsn, captainDBState.source, captainDBState.err = openCaptainDB(ctx, mode)
	captainDBState.opened = true
	captainDBState.migrated = captainDBState.err == nil && mode == captainDatabaseWithMigrations
	return captainDBState.db, captainDBState.err
}

// ConfigureNativeDatabase injects a host-owned GORM pool before Captain's CLI
// database is first used. Hosts such as Gavel use this to keep Captain session,
// prompt, and plan APIs on the same process-owned database. Reconfiguring the
// same pool is idempotent; replacing an initialized pool is rejected because
// callers may already hold handles backed by it.
func ConfigureNativeDatabase(gormDB *gorm.DB) error {
	db, err := database.Use(gormDB)
	if err != nil {
		return err
	}

	captainDBState.mu.Lock()
	defer captainDBState.mu.Unlock()
	if captainDBState.opened {
		if captainDBState.err == nil && captainDBState.db != nil && captainDBState.db.Gorm() == gormDB {
			return nil
		}
		return fmt.Errorf("native Captain database is already configured with a different pool")
	}
	captainDBState.db = db
	captainDBState.dsn = ""
	captainDBState.source = "host-provided database"
	captainDBState.err = nil
	captainDBState.opened = true
	captainDBState.migrated = true
	return nil
}

// setCaptainDBForTest injects (or, with nil, resets) the process-wide handle
// so tests run against their own embedded database instead of a configured
// DSN. Production code never calls this.
func setCaptainDBForTest(db *database.DB) {
	captainDBState.mu.Lock()
	defer captainDBState.mu.Unlock()
	captainDBState.db = db
	captainDBState.dsn = ""
	captainDBState.source = ""
	captainDBState.err = nil
	captainDBState.opened = db != nil
	captainDBState.migrated = db != nil
}

func openCaptainDB(ctx context.Context, mode captainDatabaseMode) (*database.DB, string, string, error) {
	dsn, source, err := captainDSN()
	if err != nil {
		return nil, "", "", err
	}
	log.Debugf("captain database using %s", source)
	options := []database.Option{database.WithDSN(dsn)}
	if mode == captainDatabaseWithMigrations {
		options = append(options, database.WithMigrations())
	}
	db, err := database.Open(ctx, options...)
	if err != nil {
		return nil, "", "", fmt.Errorf("open captain database (%s): %w", source, err)
	}
	return db, dsn, source, nil
}

func captainDatabaseIdentity() (dsn, source string) {
	captainDBState.mu.Lock()
	defer captainDBState.mu.Unlock()
	return captainDBState.dsn, captainDBState.source
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
	return database.LocalHostID()
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
	if dsn := strings.TrimSpace(databaseURL); dsn != "" {
		return dsn, "--" + databaseURLFlag, nil
	}
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
