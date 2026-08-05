package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/database"
	"gorm.io/gorm"
)

// secondaryMaxOpenConns caps each non-default context's pool. Reads against
// another machine's database are occasional, and a process may hold several
// handles at once, so they get a fraction of the default pool size.
const secondaryMaxOpenConns = 5

type captainDatabaseMode uint8

const (
	captainDatabaseNoMigrations captainDatabaseMode = iota
	captainDatabaseWithMigrations
)

// databaseHandle memoizes one context's connection. Only successful opens are
// memoized: a transient failure must not poison a long-lived serve process.
type databaseHandle struct {
	db       *database.DB
	dsn      string
	source   string
	migrated bool
}

// databaseRegistry holds one handle per context name. The default context is
// what used to be the process-wide singleton; every other entry is read-only.
var databaseRegistry = struct {
	mu      sync.Mutex
	handles map[string]*databaseHandle
}{handles: map[string]*databaseHandle{}}

// openContextDB resolves and memoizes the handle for one context. Migrations
// are rejected for every context but the default, so no code path can migrate
// a database captain only reads.
func openContextDB(ctx context.Context, name string, mode captainDatabaseMode) (*database.DB, error) {
	databaseRegistry.mu.Lock()
	defer databaseRegistry.mu.Unlock()

	if handle, ok := databaseRegistry.handles[name]; ok {
		if mode == captainDatabaseWithMigrations && !handle.migrated {
			return nil, errors.New("captain serve cannot migrate after the process database was opened without migrations")
		}
		return handle.db, nil
	}
	if mode == captainDatabaseWithMigrations && name != defaultDatabaseContextName {
		return nil, fmt.Errorf("captain only migrates the %q database context, not %q", defaultDatabaseContextName, name)
	}

	dsn, source, err := contextDSN(name)
	if err != nil {
		return nil, err
	}
	options := []database.Option{database.WithDSN(dsn)}
	if mode == captainDatabaseWithMigrations {
		options = append(options, database.WithMigrations())
	}
	if name != defaultDatabaseContextName {
		options = append(options, database.WithMaxOpenConns(secondaryMaxOpenConns))
	}
	log.Debugf("captain database context %q using %s", name, source)
	db, err := database.Open(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("open captain database context %q (%s): %w", name, source, err)
	}
	databaseRegistry.handles[name] = &databaseHandle{
		db:       db,
		dsn:      dsn,
		source:   source,
		migrated: mode == captainDatabaseWithMigrations,
	}
	return db, nil
}

// contextDSN resolves a context's connection string. The default keeps
// captain's established flag/env/db.json/embedded precedence; every other
// context is declared explicitly and opened read-only.
func contextDSN(name string) (dsn, source string, err error) {
	if name == defaultDatabaseContextName {
		return captainDSN()
	}
	dbContext, err := lookupDatabaseContext(name)
	if err != nil {
		return "", "", err
	}
	if !dbContext.ReadOnly {
		return dbContext.DSN, dbContext.Source, nil
	}
	readOnly, err := readOnlyDSN(dbContext.DSN)
	if err != nil {
		return "", "", fmt.Errorf("database context %q: %w", name, err)
	}
	return readOnly, dbContext.Source, nil
}

func contextDatabaseIdentity(name string) (dsn, source string) {
	databaseRegistry.mu.Lock()
	defer databaseRegistry.mu.Unlock()
	handle, ok := databaseRegistry.handles[name]
	if !ok {
		return "", ""
	}
	return handle.dsn, handle.source
}

// ConfigureNativeDatabase injects a host-owned GORM pool as the default
// database context before Captain's CLI database is first used. Hosts such as
// Gavel use this to keep Captain session, prompt, and plan APIs on the same
// process-owned database. Reconfiguring the same pool is idempotent; replacing
// an initialized pool is rejected because callers may already hold handles
// backed by it.
//
// It deliberately does not resolve captain's context configuration: a host that
// never selects a secondary context must not fail to boot over a malformed
// db.json. Secondary contexts, if any are ever requested, open their own pools
// outside the host's.
func ConfigureNativeDatabase(gormDB *gorm.DB) error {
	db, err := database.Use(gormDB)
	if err != nil {
		return err
	}

	databaseRegistry.mu.Lock()
	defer databaseRegistry.mu.Unlock()
	if handle, ok := databaseRegistry.handles[defaultDatabaseContextName]; ok {
		if handle.db != nil && handle.db.Gorm() == gormDB {
			return nil
		}
		return fmt.Errorf("native Captain database is already configured with a different pool")
	}
	databaseRegistry.handles[defaultDatabaseContextName] = &databaseHandle{
		db:       db,
		source:   "host-provided database",
		migrated: true,
	}
	return nil
}

// testDatabaseHandle describes a handle injected by a test.
type testDatabaseHandle struct {
	Name string
	DB   *database.DB
	// Source is the provenance string surfaced by contextDatabaseIdentity.
	Source string
	// Unmigrated installs the handle as if it had been opened without
	// migrations, so tests can exercise the serve-after-plain-open guard.
	Unmigrated bool
}

// setCaptainDBForTest injects (or, with nil, resets) the default context's
// handle so tests run against their own embedded database instead of a
// configured DSN. Production code never calls this.
func setCaptainDBForTest(db *database.DB) {
	setCaptainContextDBForTest(testDatabaseHandle{Name: defaultDatabaseContextName, DB: db})
}

// setCaptainContextDBForTest injects a handle under an arbitrary context name so
// tests can exercise context switching without a second postgres server.
func setCaptainContextDBForTest(handle testDatabaseHandle) {
	databaseRegistry.mu.Lock()
	defer databaseRegistry.mu.Unlock()
	if handle.DB == nil {
		delete(databaseRegistry.handles, handle.Name)
		return
	}
	databaseRegistry.handles[handle.Name] = &databaseHandle{
		db:       handle.DB,
		source:   handle.Source,
		migrated: !handle.Unmigrated,
	}
}

// resetCaptainContextsForTest drops every memoized handle and the cached
// context configuration.
func resetCaptainContextsForTest() {
	databaseRegistry.mu.Lock()
	databaseRegistry.handles = map[string]*databaseHandle{}
	databaseRegistry.mu.Unlock()
	resetDatabaseContextCache()
}
