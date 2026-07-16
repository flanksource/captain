package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

const SessionLifecyclePartial SessionLifecycleStatus = "partial"

type CreateSessionTreeInput struct {
	Root     CreateSessionInput
	Children []CreateSessionInput
}

type SessionTree struct {
	Root     Session
	Children []Session
}

func (db *DB) CreateSessionTree(ctx context.Context, input CreateSessionTreeInput) (*SessionTree, error) {
	if input.Root.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: root ID is required", ErrInvalidSession)
	}
	if input.Root.ParentSessionID != nil || input.Root.RootSessionID != nil {
		return nil, fmt.Errorf("%w: tree root must be canonical", ErrInvalidSession)
	}
	var tree SessionTree
	err := db.Transaction(ctx, func(tx *DB) error {
		root, err := tx.CreateOrGetSession(ctx, input.Root)
		if err != nil {
			return err
		}
		tree.Root = *root
		tree.Children = make([]Session, len(input.Children))
		for i := range input.Children {
			child := input.Children[i]
			if child.ID == uuid.Nil {
				return fmt.Errorf("%w: child %d ID is required", ErrInvalidSession, i+1)
			}
			if child.ParentSessionID != nil && *child.ParentSessionID != root.ID {
				return fmt.Errorf("%w: child %d parent must be root %s", ErrInvalidSession, i+1, root.ID)
			}
			child.ParentSessionID = &root.ID
			created, err := tx.CreateOrGetSession(ctx, child)
			if err != nil {
				return fmt.Errorf("create child %d: %w", i+1, err)
			}
			tree.Children[i] = *created
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create Captain session tree: %w", err)
	}
	return &tree, nil
}

func (db *DB) UpdateSessionLifecycle(ctx context.Context, id uuid.UUID, lifecycle SessionLifecycleStatus, reason string) (*Session, error) {
	if !validSessionLifecycle(lifecycle) {
		return nil, fmt.Errorf("%w: unknown lifecycle status %q", ErrInvalidSession, lifecycle)
	}
	session, err := db.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	activity := SessionActivityIdle
	return db.UpdateSessionState(ctx, UpdateSessionStateInput{
		ID: id, ExpectedVersion: session.StateVersion, LifecycleStatus: &lifecycle,
		ActivityState: &activity, StateReason: &reason,
	})
}
