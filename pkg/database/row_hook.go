package database

import (
	"context"
	"fmt"
)

// RowHookTable names a Captain table whose row changes a host may hook.
type RowHookTable string

const (
	RowHookSessions            RowHookTable = "captain_sessions"
	RowHookPromptRuns          RowHookTable = "captain_prompt_runs"
	RowHookTurnRequests        RowHookTable = "captain_turn_requests"
	RowHookPromptRunIterations RowHookTable = "captain_prompt_run_iterations"
	// The overview tables call their hooks only when a rendered column changed
	// (an insert, or an update their notify trigger lets through), after the
	// row has settled; a deleted session or run is reported on its source table.
	RowHookSessionOverview   RowHookTable = "captain_session_overview_rows"
	RowHookPromptRunOverview RowHookTable = "captain_prompt_run_overview_rows"
)

// RowHookSignature is the argument list a hook function must declare:
// (p_table text, p_op text, p_id uuid, p_session_id uuid,
// p_root_session_id uuid, p_prompt_run_id uuid) RETURNS void. The arguments
// are the captain_row_change identity (see 90_row_hooks.sql).
const RowHookSignature = "(text, text, uuid, uuid, uuid, uuid)"

// RowHook declares that Function is called, inside the writing transaction,
// for every insert, update and delete of a Table row. It is how a host that
// shares Captain's database keeps a projection exact without adding a trigger
// (or any DDL) onto Captain's tables.
type RowHook struct {
	Table RowHookTable
	// Function is the hook as a regprocedure, e.g.
	// "acme.project_row" + RowHookSignature; it is stored schema-qualified.
	Function string
	// Owner names the host that registered the hook.
	Owner string
}

// RegisterRowHook makes Captain call hook.Function for every row change of
// hook.Table, cascades included. An error the hook raises aborts the Captain
// write. Registration is idempotent (the same function only updates its owner)
// and fails for a hook Captain could not call: an unsupported table, a missing
// function, a signature other than RowHookSignature returning void, or no
// owner.
func (db *DB) RegisterRowHook(ctx context.Context, hook RowHook) error {
	return db.execRowHook(ctx, `SELECT captain_register_row_hook(?, ?::regprocedure, ?)`, hook, "register", hook.Owner)
}

// UnregisterRowHook stops Captain calling hook.Function for hook.Table. It
// fails when no such hook is registered; Owner is not consulted. A hook whose
// function was already dropped is removed by naming it exactly as
// captain_row_hooks stores it.
func (db *DB) UnregisterRowHook(ctx context.Context, hook RowHook) error {
	return db.execRowHook(ctx, `SELECT captain_unregister_row_hook(?, ?)`, hook, "unregister")
}

func (db *DB) execRowHook(ctx context.Context, query string, hook RowHook, verb string, extra ...any) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	args := append([]any{string(hook.Table), hook.Function}, extra...)
	if err := db.gorm.WithContext(ctx).Exec(query, args...).Error; err != nil {
		return fmt.Errorf("%s Captain row hook %s on %s: %w", verb, hook.Function, hook.Table, err)
	}
	return nil
}
