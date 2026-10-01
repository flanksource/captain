package database

import (
	"context"
	"fmt"
)

// DeleteGuardTable names a Captain table whose rows a host may protect.
type DeleteGuardTable string

const (
	DeleteGuardPromptRuns DeleteGuardTable = "captain_prompt_runs"
	DeleteGuardPlans      DeleteGuardTable = "captain_plans"
)

// DeleteGuard declares that HostColumn of HostTable holds ids of Table rows.
// It is how a host that shares Captain's database protects the Captain rows it
// links to without adding a foreign key (or any DDL) onto Captain's tables.
type DeleteGuard struct {
	Table DeleteGuardTable
	// HostTable is the host-owned table, schema-qualified or resolved on the
	// public search path; it is stored schema-qualified.
	HostTable string
	// HostColumn is a uuid column of HostTable holding Table ids.
	HostColumn string
	// Owner names the host in the rejection a guarded delete raises.
	Owner string
}

// RegisterDeleteGuard makes Captain reject deleting a Table row -- directly or
// through a session cascade -- while any HostTable row's HostColumn still
// holds its id, as ON DELETE RESTRICT would. The rejection is SQLSTATE 23503
// with constraint name captain_delete_guard. Registration is idempotent (the
// same host column only updates its owner) and fails for a guard Captain could
// not enforce: an unsupported table, a missing host table or column, a column
// that is not uuid, or no owner.
func (db *DB) RegisterDeleteGuard(ctx context.Context, guard DeleteGuard) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	err := db.gorm.WithContext(ctx).Exec(`SELECT captain_register_delete_guard(?, ?, ?, ?)`,
		string(guard.Table), guard.HostTable, guard.HostColumn, guard.Owner).Error
	if err != nil {
		return fmt.Errorf("register Captain delete guard %s <- %s.%s: %w",
			guard.Table, guard.HostTable, guard.HostColumn, err)
	}
	return nil
}
