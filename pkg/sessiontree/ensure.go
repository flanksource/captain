// Package sessiontree creates host-owned Captain session trees. A host (e.g. a
// TODO tracker) describes the tree it needs and Captain owns every write to
// captain_sessions; the host's own rows are written through a Link hook that
// runs in the same transaction.
package sessiontree

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrNoTransaction reports an EnsureTx call on a handle that is not scoped to
// a transaction, where a partial tree could be committed.
var ErrNoTransaction = errors.New("captain session hierarchy requires a transaction-scoped handle")

// Link writes the host's own rows for an ensured session tree. It runs inside
// the transaction that created the sessions, receives them in spec order, and
// rolls the whole tree back when it returns an error.
type Link func(ctx context.Context, tx *database.DB, sessions []*database.Session) error

// Ensure creates or gets an ordered session tree, then runs link in the same
// transaction. When db is already transaction-scoped the work nests under a
// savepoint of the caller's transaction.
//
// Each spec is a database.CreateSessionInput:
//   - a spec without ParentSessionID is an aggregate root and must resolve to a
//     canonical row (no parent, no root);
//   - a ParentSessionID names a spec earlier in the batch or a stored session;
//   - RootSessionID is always derived from the parent and must not be supplied;
//   - a spec with a nil ID gets a generated one, so only caller-supplied IDs
//     are idempotent.
//
// A spec whose ID already exists is resolved exactly like
// database.CreateOrGetSession: its Metadata is merged with `metadata || spec`
// (top-level keys from the spec replace stored keys wholesale, e.g. the whole
// `links` object), Provider is filled only when the stored label is empty, a
// parent is adopted only by an unparented row, and every other descriptive
// field (Title, InitialPrompt, Project, CWD, AgentType, Description, …) keeps
// its first-written value.
func Ensure(ctx context.Context, db *database.DB, specs []database.CreateSessionInput, link Link) ([]*database.Session, error) {
	if link == nil {
		return nil, fmt.Errorf("%w: a link hook is required; use EnsureTx inside the host's own transaction instead", database.ErrInvalidSession)
	}
	if err := validateTree(specs); err != nil {
		return nil, err
	}
	var sessions []*database.Session
	err := db.Transaction(ctx, func(tx *database.DB) error {
		var err error
		if sessions, err = ensureTree(ctx, tx, specs); err != nil {
			return err
		}
		return link(ctx, tx, sessions)
	})
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

// EnsureTx is Ensure without a link, for Captain services that compose a
// session tree into a larger transaction (prompt-run admission, plans). tx
// must be transaction-scoped, e.g. the handle database.DB.Transaction passes.
func EnsureTx(ctx context.Context, tx *database.DB, specs []database.CreateSessionInput) ([]*database.Session, error) {
	if !inTransaction(tx) {
		return nil, ErrNoTransaction
	}
	if err := validateTree(specs); err != nil {
		return nil, err
	}
	return ensureTree(ctx, tx, specs)
}

func validateTree(specs []database.CreateSessionInput) error {
	if len(specs) == 0 {
		return fmt.Errorf("%w: at least one session spec is required", database.ErrInvalidSession)
	}
	position := make(map[uuid.UUID]int, len(specs))
	for i, spec := range specs {
		if spec.RootSessionID != nil {
			return fmt.Errorf("%w: spec %d supplies a root session; the root is derived from the parent", database.ErrInvalidSession, i)
		}
		if spec.ID == uuid.Nil {
			continue
		}
		if first, duplicate := position[spec.ID]; duplicate {
			return fmt.Errorf("%w: specs %d and %d share session ID %s", database.ErrInvalidSession, first, i, spec.ID)
		}
		position[spec.ID] = i
	}
	for i, spec := range specs {
		if spec.ParentSessionID == nil {
			continue
		}
		if at, inBatch := position[*spec.ParentSessionID]; inBatch && at >= i {
			return fmt.Errorf("%w: spec %d names parent %s, which is spec %d and must come before it", database.ErrInvalidSession, i, *spec.ParentSessionID, at)
		}
	}
	return nil
}

func ensureTree(ctx context.Context, tx *database.DB, specs []database.CreateSessionInput) ([]*database.Session, error) {
	sessions := make([]*database.Session, 0, len(specs))
	for i, spec := range specs {
		session, err := tx.CreateOrGetSession(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("ensure session spec %d (%s): %w", i, spec.ID, err)
		}
		if spec.ParentSessionID == nil && (session.ParentSessionID != nil || session.RootSessionID != nil) {
			return nil, fmt.Errorf("%w: spec %d is a root but session %s is not a canonical aggregate root", database.ErrSessionConflict, i, session.ID)
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// inTransaction mirrors GORM's own nested-transaction check: a transaction
// handle's connection pool is a *sql.Tx, which commits.
func inTransaction(db *database.DB) bool {
	handle := db.Gorm()
	if handle == nil || handle.Statement == nil {
		return false
	}
	committer, ok := handle.Statement.ConnPool.(gorm.TxCommitter)
	return ok && committer != nil
}
