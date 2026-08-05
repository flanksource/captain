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
	"github.com/spf13/pflag"
)

const (
	databaseURLFlag     = "db-url"
	databaseContextFlag = "context"
)

var (
	databaseURLs             []string
	databaseContextFlagValue string
)

// BindDatabaseFlags exposes database selection as root persistent flags. A bare
// --db-url overrides the monitored database; name=URL declares an additional
// read-only context, which --context then selects.
func BindDatabaseFlags(flags *pflag.FlagSet) {
	flags.StringArrayVar(&databaseURLs, databaseURLFlag, nil,
		"PostgreSQL database URL, or name=URL to declare an additional read-only context (repeatable)")
	flags.StringVar(&databaseContextFlagValue, databaseContextFlag, "",
		"Database context to read from (default: the monitored database)")
}

// captainDB opens the database for the active context. Reads use this; writes
// must use captainDefaultDB.
func captainDB(ctx context.Context) (*database.DB, error) {
	return openContextDB(ctx, activeDatabaseContextName(ctx), captainDatabaseNoMigrations)
}

// captainDefaultDB opens the monitored database regardless of the active
// context. Every write goes through it, because captain only ever owns the
// default context's data.
func captainDefaultDB(ctx context.Context) (*database.DB, error) {
	return openContextDB(ctx, defaultDatabaseContextName, captainDatabaseNoMigrations)
}

// captainServeDB opens and migrates the monitored database. It is the only
// migrating path in the process.
func captainServeDB(ctx context.Context) (*database.DB, error) {
	return openContextDB(ctx, defaultDatabaseContextName, captainDatabaseWithMigrations)
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
//
// Monitoring is a property of this machine and the database it writes, so a
// non-default context is returned as read: a read of another machine's
// database must never write to it.
func freshenSessionDB(ctx context.Context) (*database.DB, error) {
	name := activeDatabaseContextName(ctx)
	db, err := openContextDB(ctx, name, captainDatabaseNoMigrations)
	if err != nil {
		return nil, err
	}
	if name != defaultDatabaseContextName {
		return db, nil
	}
	config := monitor.Config{DB: db, HostID: captainHostID(), DiscoverProcesses: monitorDiscoverProcesses}
	if err := monitor.RunOnce(ctx, config); err != nil {
		return nil, fmt.Errorf("refresh session database: %w", err)
	}
	return db, nil
}

// captainDSN resolves the default context's connection: explicit env DSNs
// first, then a gavel-shared database, finally captain's own shared embedded
// postgres.
func captainDSN() (dsn, source string, err error) {
	override, err := defaultDatabaseURLOverride()
	if err != nil {
		return "", "", err
	}
	if override != "" {
		return override, "--" + databaseURLFlag, nil
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
