package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// RowChangeChannel is the PostgreSQL channel migration 85 notifies for every
// committed insert, update and delete of the rows a host projects Captain
// execution from. See 85_row_change_notify.sql for the payload contract.
const RowChangeChannel = "captain_row_change"

// RowChangeTable names a Captain table that notifies RowChangeChannel.
type RowChangeTable string

const (
	RowChangeSessions            RowChangeTable = "captain_sessions"
	RowChangePromptRuns          RowChangeTable = "captain_prompt_runs"
	RowChangeTurnRequests        RowChangeTable = "captain_turn_requests"
	RowChangePromptRunIterations RowChangeTable = "captain_prompt_run_iterations"
)

// RowChangeOp is the statement that changed the row.
type RowChangeOp string

const (
	RowChangeInsert RowChangeOp = "INSERT"
	RowChangeUpdate RowChangeOp = "UPDATE"
	RowChangeDelete RowChangeOp = "DELETE"
)

// RowChange identifies one changed Captain row. It carries identity only:
// PostgreSQL folds a transaction's identical notifications into one, so a
// listener re-reads the row (a deleted row is gone and is identified only).
//
// SessionID is set for sessions (the row itself), prompt runs and turn
// requests. RootSessionID is set for sessions (the family root, the row itself
// for a root) and prompt runs. PromptRunID is set for prompt runs (the row
// itself), iterations and turn requests that belong to a run. An update that
// moves a row between sessions or runs is delivered once for each identity.
type RowChange struct {
	Table         RowChangeTable `json:"table"`
	Op            RowChangeOp    `json:"op"`
	ID            uuid.UUID      `json:"id"`
	SessionID     *uuid.UUID     `json:"sessionId,omitempty"`
	RootSessionID *uuid.UUID     `json:"rootSessionId,omitempty"`
	PromptRunID   *uuid.UUID     `json:"promptRunId,omitempty"`
}

// RowChangeListener receives RowChangeChannel notifications. Both callbacks
// are required and run on the listening goroutine; an error from either stops
// ListenRowChanges and is returned from it.
type RowChangeListener struct {
	// OnChange is called for every decoded change, in delivery order.
	OnChange func(context.Context, RowChange) error
	// OnResync is called once the LISTEN is established and again after every
	// reconnect. Nothing is delivered while no LISTEN is active, so a listener
	// re-reads everything it projects here.
	OnResync func(context.Context) error
}

// ListenRowChanges LISTENs on RowChangeChannel over a dedicated connection
// from the handle's pool and blocks until ctx is cancelled (returning
// ctx.Err()), a callback fails, a payload violates the contract, or a lost
// connection cannot be re-established.
func (db *DB) ListenRowChanges(ctx context.Context, listener RowChangeListener) error {
	if listener.OnChange == nil {
		return errors.New("listen Captain row changes: OnChange is required")
	}
	if listener.OnResync == nil {
		return errors.New("listen Captain row changes: OnResync is required")
	}
	if err := db.requireGorm(); err != nil {
		return err
	}
	pool, err := db.gorm.DB()
	if err != nil {
		return fmt.Errorf("listen Captain row changes: database handle has no connection pool: %w", err)
	}
	connection, err := Listen(ctx, pool, RowChangeChannel)
	if err != nil {
		return err
	}
	resync := func() error { return listener.OnResync(ctx) }
	if err := resync(); err != nil {
		connection.release()
		return err
	}
	return connection.Run(ctx, func(payload string) error {
		change, err := decodeRowChange(payload)
		if err != nil {
			return err
		}
		return listener.OnChange(ctx, change)
	}, resync)
}

func decodeRowChange(payload string) (RowChange, error) {
	var change RowChange
	if err := json.Unmarshal([]byte(payload), &change); err != nil {
		return RowChange{}, fmt.Errorf("decode %s payload %q: %w", RowChangeChannel, payload, err)
	}
	switch change.Table {
	case RowChangeSessions, RowChangePromptRuns, RowChangeTurnRequests, RowChangePromptRunIterations:
	default:
		return RowChange{}, fmt.Errorf("%s payload %q: unknown table %q", RowChangeChannel, payload, change.Table)
	}
	switch change.Op {
	case RowChangeInsert, RowChangeUpdate, RowChangeDelete:
	default:
		return RowChange{}, fmt.Errorf("%s payload %q: unknown op %q", RowChangeChannel, payload, change.Op)
	}
	if change.ID == uuid.Nil {
		return RowChange{}, fmt.Errorf("%s payload %q has no id", RowChangeChannel, payload)
	}
	return change, nil
}
