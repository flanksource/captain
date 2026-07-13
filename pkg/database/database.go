// Package database provides Captain's public database integration seam.
//
// Captain owns its migrations and schema. A host such as Gavel may provide its
// existing GORM pool through Config.Gorm while also supplying the pool's DSN so
// Captain can apply its own migration bundle first. Standalone consumers can
// omit Config.Gorm and Captain will open a pool after applying the same bundle.
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/migrations"
	commonsdb "github.com/flanksource/commons-db/db"
	"gorm.io/gorm"
)

// Config selects an injected or Captain-opened database connection.
type Config struct {
	// Gorm is an optional shared application pool. Captain never closes an
	// injected pool.
	Gorm *gorm.DB
	// DSN is required when Captain should apply its migrations. It is also used
	// to open a standalone pool when Gorm is nil.
	DSN string
}

// DB is a Captain database handle. It records pool ownership so a host can
// safely share its application pool without Captain closing it.
type DB struct {
	gorm  *gorm.DB
	owned bool
}

type dependencies struct {
	migrate func(context.Context, string) error
	open    func(string, *gorm.Config) (*gorm.DB, error)
}

var defaultDependencies = dependencies{
	migrate: migrations.Apply,
	open:    commonsdb.NewGorm,
}

// Open applies Captain's migrations when a DSN is supplied, then reuses the
// injected GORM pool or opens a Captain-owned pool. Migration always precedes
// opening a standalone pool, making the ordering explicit for hosts that apply
// several independently owned schemas to one database.
//
// An injected pool may be used without a DSN when the host has already called
// Migrate. Supplying neither a pool nor a DSN is an error.
func Open(ctx context.Context, config Config) (*DB, error) {
	return open(ctx, config, defaultDependencies)
}

func open(ctx context.Context, config Config, deps dependencies) (*DB, error) {
	dsn := strings.TrimSpace(config.DSN)
	if config.Gorm == nil && dsn == "" {
		return nil, errors.New("captain database requires an injected GORM pool or DSN")
	}

	if dsn != "" {
		if err := deps.migrate(ctx, dsn); err != nil {
			return nil, err
		}
	}
	if config.Gorm != nil {
		return &DB{gorm: config.Gorm}, nil
	}

	gormDB, err := deps.open(dsn, commonsdb.DefaultGormConfig())
	if err != nil {
		return nil, fmt.Errorf("open Captain database: %w", err)
	}
	return &DB{gorm: gormDB, owned: true}, nil
}

// Use wraps an already migrated shared GORM pool. The returned handle does not
// own or close the pool. Use Open with both Gorm and DSN when Captain should
// apply its schema before reusing the pool.
func Use(gormDB *gorm.DB) (*DB, error) {
	if gormDB == nil {
		return nil, errors.New("captain database GORM pool is nil")
	}
	return &DB{gorm: gormDB}, nil
}

// Migrate applies Captain's authoritative HCL and SQL migration bundle without
// opening another application pool.
func Migrate(ctx context.Context, dsn string) error {
	return migrations.Apply(ctx, dsn)
}

// MigrateWithLegacySessionCutover explicitly archives and backfills the
// path-keyed session summary cache before applying Captain's authoritative HCL
// schema. Normal Migrate remains fail-closed when it encounters the legacy
// shape so hosts must opt into the data conversion deliberately.
func MigrateWithLegacySessionCutover(ctx context.Context, dsn string) (*migrations.LegacySessionCutoverReport, error) {
	return migrations.ApplyWithLegacySessionCutover(ctx, dsn)
}

// Gorm returns the application pool supplied to or opened by Captain.
func (db *DB) Gorm() *gorm.DB {
	if db == nil {
		return nil
	}
	return db.gorm
}

// Transaction runs fn with a Captain handle backed by the same GORM
// transaction. The scoped handle never owns or closes the underlying pool, so
// hosts can atomically update Captain rows and their own rows in one database.
func (db *DB) Transaction(ctx context.Context, fn func(*DB) error) error {
	if db == nil || db.gorm == nil {
		return errors.New("captain database is not initialized")
	}
	if fn == nil {
		return errors.New("captain database transaction callback is nil")
	}
	return db.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&DB{gorm: tx})
	})
}

// Close releases a pool opened by Captain. It is a no-op for injected pools.
func (db *DB) Close() error {
	if db == nil || db.gorm == nil || !db.owned {
		return nil
	}
	sqlDB, err := db.gorm.DB()
	if err != nil {
		return fmt.Errorf("access Captain SQL pool: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("close Captain database: %w", err)
	}
	return nil
}
