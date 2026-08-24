// Package database provides Captain's public database integration seam.
//
// Captain owns its migrations and schema. Opens are non-migrating by default;
// controlled application startup must opt in with WithMigrations.
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/migrations"
	commonsdb "github.com/flanksource/commons-db/db"
	commonsmigrate "github.com/flanksource/commons-db/migrate"
	"gorm.io/gorm"
)

// Option configures a Captain database open.
type Option func(*openOptions)

type openOptions struct {
	dsn          string
	gorm         *gorm.DB
	gormSet      bool
	migrate      bool
	schema       string
	maxOpenConns int
}

// WithDSN selects the PostgreSQL connection string.
func WithDSN(dsn string) Option {
	return func(options *openOptions) { options.dsn = dsn }
}

// WithGorm selects a host-owned application pool. Captain never closes it.
func WithGorm(gormDB *gorm.DB) Option {
	return func(options *openOptions) {
		options.gorm = gormDB
		options.gormSet = true
	}
}

// WithMigrations applies Captain's schema before returning the database.
func WithMigrations() Option {
	return func(options *openOptions) { options.migrate = true }
}

// WithSchema selects the validated PostgreSQL schema owned by this Captain handle.
func WithSchema(name string) Option {
	return func(options *openOptions) { options.schema = name }
}

// WithMaxOpenConns caps a Captain-owned pool. Processes that hold several
// database handles at once use it so the pools do not add up to an unreasonable
// number of backends. Ignored for injected pools, which the host sizes.
func WithMaxOpenConns(conns int) Option {
	return func(options *openOptions) { options.maxOpenConns = conns }
}

// DB is a Captain database handle. It records pool ownership so a host can
// safely share its application pool without Captain closing it.
type DB struct {
	gorm   *gorm.DB
	owned  bool
	schema string
}

type dependencies struct {
	migrate func(context.Context, string, string) error
	open    func(string, *gorm.Config) (*gorm.DB, error)
}

var defaultDependencies = dependencies{
	migrate: func(ctx context.Context, dsn, schemaName string) error {
		return migrations.Apply(ctx, dsn, migrations.WithSchema(schemaName))
	},
	open: commonsdb.NewGorm,
}

// Open reuses an injected pool or opens a Captain-owned pool. It does not
// migrate unless WithMigrations is supplied.
func Open(ctx context.Context, options ...Option) (*DB, error) {
	return open(ctx, defaultDependencies, options...)
}

func open(ctx context.Context, deps dependencies, optionFns ...Option) (*DB, error) {
	options := openOptions{schema: migrations.DefaultSchema}
	for _, option := range optionFns {
		if option != nil {
			option(&options)
		}
	}
	dsn := strings.TrimSpace(options.dsn)
	if err := commonsmigrate.ValidateSchemaName(options.schema); err != nil {
		return nil, fmt.Errorf("captain database schema: %w", err)
	}
	if options.gormSet && options.gorm == nil {
		return nil, errors.New("captain database GORM pool is nil")
	}
	if options.gorm == nil && dsn == "" {
		return nil, errors.New("captain database requires an injected GORM pool or DSN")
	}
	if options.migrate && dsn == "" {
		return nil, errors.New("captain database migrations require a DSN")
	}
	if options.gorm != nil && options.schema != migrations.DefaultSchema {
		return nil, fmt.Errorf("captain database cannot select schema %q on a host-owned GORM pool", options.schema)
	}
	if options.migrate {
		if err := deps.migrate(ctx, dsn, options.schema); err != nil {
			return nil, err
		}
	}
	if options.gorm != nil {
		return &DB{gorm: options.gorm, schema: options.schema}, nil
	}

	scopedDSN := dsn
	if options.schema != migrations.DefaultSchema {
		var err error
		scopedDSN, err = commonsmigrate.ConnectionForSchema(dsn, options.schema)
		if err != nil {
			return nil, fmt.Errorf("scope Captain database: %w", err)
		}
	}
	gormDB, err := deps.open(scopedDSN, commonsdb.DefaultGormConfig())
	if err != nil {
		return nil, fmt.Errorf("open Captain database: %w", err)
	}
	if options.maxOpenConns > 0 {
		sqlDB, err := gormDB.DB()
		if err != nil {
			return nil, fmt.Errorf("access Captain SQL pool: %w", err)
		}
		sqlDB.SetMaxOpenConns(options.maxOpenConns)
		sqlDB.SetMaxIdleConns(options.maxOpenConns)
	}
	return &DB{gorm: gormDB, owned: true, schema: options.schema}, nil
}

// Use wraps an already migrated shared GORM pool. The returned handle does not
// own or close the pool.
func Use(gormDB *gorm.DB) (*DB, error) {
	if gormDB == nil {
		return nil, errors.New("captain database GORM pool is nil")
	}
	return &DB{gorm: gormDB, schema: migrations.DefaultSchema}, nil
}

// Migrate applies Captain's authoritative HCL and SQL migration bundle without
// opening another application pool.
func Migrate(ctx context.Context, dsn string, options ...Option) error {
	config := openOptions{schema: migrations.DefaultSchema}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return migrations.Apply(ctx, dsn, migrations.WithSchema(config.schema))
}

// Gorm returns the application pool supplied to or opened by Captain.
func (db *DB) Gorm() *gorm.DB {
	if db == nil {
		return nil
	}
	return db.gorm
}

// Schema returns the PostgreSQL schema owned by the handle.
func (db *DB) Schema() string {
	if db == nil {
		return ""
	}
	return db.schema
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
		return fn(&DB{gorm: tx, schema: db.schema})
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
