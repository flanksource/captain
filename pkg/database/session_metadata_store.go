package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SetSessionMetadataOnce binds one metadata key to its first value. Replaying
// the same value is idempotent; replacing it is an identity conflict.
func (db *DB) SetSessionMetadataOnce(ctx context.Context, id uuid.UUID, key string, value any) error {
	key = strings.TrimSpace(key)
	if id == uuid.Nil || key == "" {
		return fmt.Errorf("%w: session ID and metadata key are required", ErrInvalidSession)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode Captain session metadata %q: %w", key, err)
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return fmt.Errorf("normalize Captain session metadata %q: %w", key, err)
	}
	return db.Transaction(ctx, func(tx *DB) error {
		var record sessionRecord
		if err := tx.gorm.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&record, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %s", ErrSessionNotFound, id)
			}
			return fmt.Errorf("lock Captain session metadata: %w", err)
		}
		if current, exists := record.Metadata[key]; exists {
			currentJSON, marshalErr := json.Marshal(current)
			if marshalErr != nil {
				return fmt.Errorf("encode existing Captain session metadata %q: %w", key, marshalErr)
			}
			var currentNormalized any
			if err := json.Unmarshal(currentJSON, &currentNormalized); err != nil {
				return fmt.Errorf("normalize existing Captain session metadata %q: %w", key, err)
			}
			if reflect.DeepEqual(currentNormalized, normalized) {
				return nil
			}
			return fmt.Errorf("%w: session metadata %q is already bound", ErrSessionConflict, key)
		}
		if record.Metadata == nil {
			record.Metadata = map[string]any{}
		}
		record.Metadata[key] = normalized
		if err := tx.gorm.WithContext(ctx).Model(&sessionRecord{}).Where("id = ?", id).
			Update("metadata", record.Metadata).Error; err != nil {
			return fmt.Errorf("set Captain session metadata %q: %w", key, err)
		}
		return nil
	})
}
