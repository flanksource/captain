package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (db *DB) reconcileSessionHierarchy(ctx context.Context, existing *sessionRecord, requested sessionRecord) error {
	if requested.ParentSessionID == nil && requested.RootSessionID == nil {
		return nil
	}
	if equalOptionalUUID(existing.ParentSessionID, requested.ParentSessionID) &&
		equalOptionalUUID(existing.RootSessionID, requested.RootSessionID) {
		return nil
	}
	if existing.ParentSessionID != nil || existing.RootSessionID != nil {
		return fmt.Errorf("%w: existing session has a different hierarchy", ErrSessionConflict)
	}
	adopted, err := db.adoptSessionHierarchy(ctx, existing.ID, requested)
	if err != nil {
		return err
	}
	if adopted {
		existing.ParentSessionID, existing.RootSessionID = requested.ParentSessionID, requested.RootSessionID
		return nil
	}
	current, err := db.GetSession(ctx, existing.ID)
	if err != nil {
		return err
	}
	if !equalOptionalUUID(current.ParentSessionID, requested.ParentSessionID) ||
		!equalOptionalUUID(current.RootSessionID, requested.RootSessionID) {
		return fmt.Errorf("%w: existing session has a different hierarchy", ErrSessionConflict)
	}
	return nil
}

func (db *DB) adoptSessionHierarchy(ctx context.Context, sessionID uuid.UUID, requested sessionRecord) (bool, error) {
	adopted := false
	err := db.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&sessionRecord{}).
			Where("id = ? AND parent_session_id IS NULL AND root_session_id IS NULL", sessionID).
			Updates(map[string]any{
				"parent_session_id": requested.ParentSessionID,
				"root_session_id":   requested.RootSessionID,
			})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		adopted = true
		if err := tx.Model(&sessionRecord{}).
			Where("root_session_id = ?", sessionID).
			Update("root_session_id", requested.RootSessionID).Error; err != nil {
			return err
		}
		return tx.Model(&promptRunRecord{}).
			Where("root_session_id = ?", sessionID).
			Update("root_session_id", requested.RootSessionID).Error
	})
	if err != nil {
		return false, fmt.Errorf("adopt Captain session hierarchy: %w", err)
	}
	return adopted, nil
}

func sessionAggregateRoot(session *Session) uuid.UUID {
	if session != nil && session.RootSessionID != nil {
		return *session.RootSessionID
	}
	if session == nil {
		return uuid.Nil
	}
	return session.ID
}
