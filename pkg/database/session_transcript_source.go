package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func (db *DB) GetTranscriptSession(ctx context.Context, parentID uuid.UUID) (*Session, error) {
	if parentID == uuid.Nil {
		return nil, fmt.Errorf("%w: parent session ID is required", ErrInvalidSession)
	}
	var records []sessionRecord
	err := db.gorm.WithContext(ctx).
		Where("parent_session_id = ? AND parent_relation = ?", parentID, SessionParentRelationTranscript).
		Order("created_at, id").Limit(2).Find(&records).Error
	if err != nil {
		return nil, fmt.Errorf("find provider transcript session: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: transcript child of %s", ErrSessionNotFound, parentID)
	}
	if len(records) > 1 {
		return nil, fmt.Errorf("%w: session %s has multiple transcript children", ErrSessionConflict, parentID)
	}
	result := sessionFromRecord(records[0])
	return &result, nil
}

func (db *DB) RegisterSessionSource(ctx context.Context, sessionID uuid.UUID, input IngestSourceInput) error {
	if sessionID == uuid.Nil || strings.TrimSpace(input.SourceKind) == "" || strings.TrimSpace(input.Path) == "" || input.ParserVersion <= 0 {
		return fmt.Errorf("%w: session, source kind, path, and parser version are required", ErrInvalidIngest)
	}
	if _, err := db.GetSession(ctx, sessionID); err != nil {
		return err
	}
	return db.upsertSessionSource(ctx, sessionID, input)
}
