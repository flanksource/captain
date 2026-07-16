// Package migrations embeds and applies Captain's authoritative PostgreSQL
// schema. Consumers that share a database with Captain should call Apply before
// creating cross-schema references to Captain-owned tables.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	commonsmigrate "github.com/flanksource/commons-db/migrate"
)

const Scope = "captain"

const (
	// captainMigrationLockNamespace and captainMigrationLockKey are stable,
	// Captain-specific PostgreSQL advisory-lock identifiers ("CAPT", "MIGR").
	// Gavel uses a different outer lifecycle key, so a host can safely hold its
	// own lock while Captain serializes this independently owned migration scope.
	captainMigrationLockNamespace int32 = 0x43415054
	captainMigrationLockKey       int32 = 0x4d494752

	migrationUnlockTimeout = 5 * time.Second
)

// schemaFS contains the complete Captain-owned schema. SQL files are colocated
// with the HCL so commons-db/migrate applies their declared pre/post phases in
// the same migration scope.
//
//go:embed *.hcl *.sql
var schemaFS embed.FS

type migrationLockHandle interface {
	Close() error
}

type applyDependencies struct {
	acquireLock func(context.Context, string) (migrationLockHandle, error)
	migrate     func(context.Context, string) error
}

var defaultApplyDependencies = applyDependencies{
	acquireLock: acquireMigrationLock,
	migrate: func(ctx context.Context, connection string) error {
		return commonsmigrate.Apply(ctx, connection, schemaFS,
			commonsmigrate.WithName(Scope),
			commonsmigrate.WithExclude("todo_*"),
		)
	},
}

// Apply reconciles the checked-in HCL schema, then applies the colocated SQL
// migrations. A Captain-specific session advisory lock serializes the complete
// migration bundle across processes. It is safe to call repeatedly and uses a
// stable scope so Captain can share a database with other independently
// migrated applications.
func Apply(ctx context.Context, connection string) error {
	return apply(ctx, connection, defaultApplyDependencies)
}

func apply(ctx context.Context, connection string, deps applyDependencies) (resultErr error) {
	if strings.TrimSpace(connection) == "" {
		return errors.New("captain migration connection string is empty")
	}

	lock, err := deps.acquireLock(ctx, connection)
	if err != nil {
		return fmt.Errorf("acquire Captain migration lock: %w", err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release Captain migration lock: %w", err))
		}
	}()

	if err := deps.migrate(ctx, connection); err != nil {
		return fmt.Errorf("migrate Captain database: %w", err)
	}
	return nil
}

type migrationLock struct {
	db   *sql.DB
	conn *sql.Conn

	once sync.Once
	err  error
}

func acquireMigrationLock(ctx context.Context, connection string) (migrationLockHandle, error) {
	db, err := commonsdb.NewDB(connection)
	if err != nil {
		return nil, fmt.Errorf("open advisory-lock database: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("reserve advisory-lock connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1, $2)`,
		captainMigrationLockNamespace, captainMigrationLockKey); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("lock Captain migration scope: %w", err)
	}
	return &migrationLock{db: db, conn: conn}, nil
}

func (lock *migrationLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.once.Do(func() {
		var cleanupErrors []error
		if lock.conn != nil {
			ctx, cancel := context.WithTimeout(context.Background(), migrationUnlockTimeout)
			var unlocked bool
			if err := lock.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1, $2)`,
				captainMigrationLockNamespace, captainMigrationLockKey).Scan(&unlocked); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("unlock Captain migration scope: %w", err))
			} else if !unlocked {
				cleanupErrors = append(cleanupErrors, errors.New("captain migration advisory lock was not held"))
			}
			cancel()
			if err := lock.conn.Close(); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("close Captain migration lock connection: %w", err))
			}
		}
		if lock.db != nil {
			if err := lock.db.Close(); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("close Captain migration lock database: %w", err))
			}
		}
		lock.err = errors.Join(cleanupErrors...)
	})
	return lock.err
}
