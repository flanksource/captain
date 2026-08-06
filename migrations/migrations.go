// Package migrations embeds and applies Captain's authoritative PostgreSQL
// schema. Consumers that share a database with Captain should call Apply before
// creating cross-schema references to Captain-owned tables.
package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	commonsmigrate "github.com/flanksource/commons-db/migrate"
)

const Scope = "captain"
const DefaultSchema = "public"

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
	acquireLock func(context.Context, applyRequest) (migrationLockHandle, error)
	migrate     func(context.Context, applyRequest) error
	verify      func(context.Context, applyRequest) error
}

type applyRequest struct {
	Connection string
	Schema     string
}

type options struct {
	schema string
}

// Option configures Captain's migration bundle.
type Option func(*options)

// WithSchema selects the schema that owns Captain's migration bundle.
func WithSchema(name string) Option {
	return func(options *options) { options.schema = name }
}

var defaultApplyDependencies = applyDependencies{
	acquireLock: acquireMigrationLock,
	migrate: func(ctx context.Context, request applyRequest) error {
		filesystem, err := schemaFilesystem(request.Schema)
		if err != nil {
			return err
		}
		return commonsmigrate.Apply(ctx, request.Connection, filesystem,
			commonsmigrate.WithName(Scope),
			commonsmigrate.WithSchema(request.Schema),
			commonsmigrate.WithExclude("todo_*"),
		)
	},
	verify: verifyToolApprovalIdentity,
}

// Apply reconciles the checked-in HCL schema, then applies the colocated SQL
// migrations. A Captain-specific session advisory lock serializes the complete
// migration bundle across processes. It is safe to call repeatedly and uses a
// stable scope so Captain can share a database with other independently
// migrated applications.
func Apply(ctx context.Context, connection string, optionFns ...Option) error {
	config := options{schema: DefaultSchema}
	for _, option := range optionFns {
		if option != nil {
			option(&config)
		}
	}
	return apply(ctx, applyRequest{Connection: connection, Schema: config.schema}, defaultApplyDependencies)
}

func apply(ctx context.Context, request applyRequest, deps applyDependencies) (resultErr error) {
	if strings.TrimSpace(request.Connection) == "" {
		return errors.New("captain migration connection string is empty")
	}
	if err := commonsmigrate.ValidateSchemaName(request.Schema); err != nil {
		return fmt.Errorf("captain migration schema: %w", err)
	}

	lock, err := deps.acquireLock(ctx, request)
	if err != nil {
		return fmt.Errorf("acquire Captain migration lock: %w", err)
	}
	defer func() {
		if err := lock.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release Captain migration lock: %w", err))
		}
	}()

	if err := deps.migrate(ctx, request); err != nil {
		return fmt.Errorf("migrate Captain database: %w", err)
	}
	if err := deps.verify(ctx, request); err != nil {
		return fmt.Errorf("verify Captain database: %w", err)
	}
	return nil
}

func verifyToolApprovalIdentity(ctx context.Context, request applyRequest) (resultErr error) {
	connection, err := commonsmigrate.ConnectionForSchema(request.Connection, request.Schema)
	if err != nil {
		return fmt.Errorf("scope schema verification database: %w", err)
	}
	db, err := commonsdb.NewDB(connection)
	if err != nil {
		return fmt.Errorf("open schema verification database: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close schema verification database: %w", err))
		}
	}()

	var validated bool
	var definition string
	err = db.QueryRowContext(ctx, `
		SELECT c.convalidated, pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class relation ON relation.oid = c.conrelid
		JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = $1
		  AND relation.relname = 'captain_turn_requests'
		  AND c.conname = 'captain_turn_requests_tool_approval_identity'
		  AND c.contype = 'c'
	`, request.Schema).Scan(&validated, &definition)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("captain_turn_requests_tool_approval_identity constraint is missing")
	}
	if err != nil {
		return fmt.Errorf("read captain_turn_requests_tool_approval_identity: %w", err)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(definition), " "))
	if !validated || strings.Contains(normalized, "credential_id is not null") {
		return fmt.Errorf("captain_turn_requests_tool_approval_identity is invalid: %s", definition)
	}
	for _, required := range []string{
		"prompt_run_id is not null", "turn_id is not null",
		"model_call_id is not null", "tool_call_id is not null",
	} {
		if !strings.Contains(normalized, required) {
			return fmt.Errorf("captain_turn_requests_tool_approval_identity omits %q: %s", required, definition)
		}
	}
	return nil
}

type migrationLock struct {
	db   *sql.DB
	conn *sql.Conn
	key  int32

	once sync.Once
	err  error
}

func acquireMigrationLock(ctx context.Context, request applyRequest) (migrationLockHandle, error) {
	db, err := commonsdb.NewDB(request.Connection)
	if err != nil {
		return nil, fmt.Errorf("open advisory-lock database: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("reserve advisory-lock connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1, $2)`,
		captainMigrationLockNamespace, migrationLockKey(request.Schema)); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("lock Captain migration scope: %w", err)
	}
	return &migrationLock{db: db, conn: conn, key: migrationLockKey(request.Schema)}, nil
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
				captainMigrationLockNamespace, lock.key).Scan(&unlocked); err != nil {
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

func migrationLockKey(schemaName string) int32 {
	if schemaName == DefaultSchema {
		return captainMigrationLockKey
	}
	digest := sha256.Sum256([]byte(schemaName))
	return int32(binary.BigEndian.Uint32(digest[:4]))
}
