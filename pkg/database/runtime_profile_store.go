// Storage for reusable runtime presets. Profiles, which compose presets, live in
// runtime_profile_store_profiles.go and share the sentinels and helpers here.
//
// Names are unique case-insensitively (enforced by the lower(name) index) because
// records are referenced by name from profiles, prompt frontmatter and CLI
// flags, where two spellings of one name would be indistinguishable.

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrRuntimePresetNotFound  = errors.New("captain runtime preset not found")
	ErrRuntimeProfileNotFound = errors.New("captain runtime profile not found")
	ErrRuntimeNameTaken       = errors.New("captain runtime name is already taken")
	ErrRuntimeInvalid         = errors.New("invalid captain runtime record")
)

type runtimePresetRecord struct {
	ID          uuid.UUID             `gorm:"column:id;type:uuid;primaryKey"`
	Name        string                `gorm:"column:name"`
	Description *string               `gorm:"column:description"`
	Scope       api.SpecLayerScope    `gorm:"column:scope"`
	Spec        api.RuntimePresetSpec `gorm:"column:spec;serializer:json;type:jsonb"`
	CreatedAt   time.Time             `gorm:"column:created_at"`
	UpdatedAt   time.Time             `gorm:"column:updated_at"`
}

func (runtimePresetRecord) TableName() string { return "captain_runtime_presets" }

// RuntimePreset is a stored preset as listings and the catalog see it.
type RuntimePreset struct {
	ID          uuid.UUID             `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Scope       api.SpecLayerScope    `json:"scope"`
	Spec        api.RuntimePresetSpec `json:"spec"`
	CreatedAt   time.Time             `json:"createdAt"`
	UpdatedAt   time.Time             `json:"updatedAt"`
}

// RuntimePresetInput is everything a caller authors; ids and timestamps are
// assigned by the store.
type RuntimePresetInput struct {
	Name        string
	Description string
	Scope       api.SpecLayerScope
	Spec        api.RuntimePresetSpec
}

// ListRuntimePresets returns every preset ordered by name, always as a slice so
// an empty catalog marshals as [].
func (db *DB) ListRuntimePresets(ctx context.Context) ([]RuntimePreset, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var records []runtimePresetRecord
	if err := db.gorm.WithContext(ctx).Order("lower(name), id").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list captain runtime presets: %w", err)
	}
	presets := make([]RuntimePreset, 0, len(records))
	for _, record := range records {
		presets = append(presets, record.toPreset())
	}
	return presets, nil
}

func (db *DB) GetRuntimePreset(ctx context.Context, id uuid.UUID) (*RuntimePreset, error) {
	return db.findRuntimePreset(ctx, "id = ?", id)
}

// FindRuntimePresetByName resolves a preset by its case-insensitive name.
func (db *DB) FindRuntimePresetByName(ctx context.Context, name string) (*RuntimePreset, error) {
	return db.findRuntimePreset(ctx, "lower(name) = lower(?)", strings.TrimSpace(name))
}

func (db *DB) findRuntimePreset(ctx context.Context, query string, arg any) (*RuntimePreset, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var record runtimePresetRecord
	if err := db.gorm.WithContext(ctx).First(&record, query, arg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %v", ErrRuntimePresetNotFound, arg)
		}
		return nil, fmt.Errorf("get captain runtime preset: %w", err)
	}
	preset := record.toPreset()
	return &preset, nil
}

func (db *DB) CreateRuntimePreset(ctx context.Context, input RuntimePresetInput) (*RuntimePreset, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	record, err := runtimePresetRecordFrom(uuid.New(), input)
	if err != nil {
		return nil, err
	}
	if err := db.gorm.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, runtimeWriteError("create captain runtime preset", record.Name, err)
	}
	return db.GetRuntimePreset(ctx, record.ID)
}

// UpdateRuntimePreset replaces every authored field and bumps updated_at.
func (db *DB) UpdateRuntimePreset(ctx context.Context, id uuid.UUID, input RuntimePresetInput) (*RuntimePreset, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	record, err := runtimePresetRecordFrom(id, input)
	if err != nil {
		return nil, err
	}
	spec, err := json.Marshal(record.Spec)
	if err != nil {
		return nil, fmt.Errorf("encode captain runtime preset spec: %w", err)
	}
	result := db.gorm.WithContext(ctx).Model(&runtimePresetRecord{}).Where("id = ?", id).Updates(map[string]any{
		"name": record.Name, "description": record.Description, "scope": record.Scope,
		"spec": gorm.Expr("?::jsonb", string(spec)), "updated_at": clause.Expr{SQL: "now()"},
	})
	if result.Error != nil {
		return nil, runtimeWriteError("update captain runtime preset", record.Name, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: %s", ErrRuntimePresetNotFound, id)
	}
	return db.GetRuntimePreset(ctx, id)
}

func (db *DB) DeleteRuntimePreset(ctx context.Context, id uuid.UUID) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	result := db.gorm.WithContext(ctx).Delete(&runtimePresetRecord{}, "id = ?", id)
	if result.Error != nil {
		return fmt.Errorf("delete captain runtime preset: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", ErrRuntimePresetNotFound, id)
	}
	return nil
}

// runtimePresetRecordFrom validates the input the way the catalog resolver
// will, so an unusable preset is refused at write time rather than at the first
// profile that references it.
func runtimePresetRecordFrom(id uuid.UUID, input RuntimePresetInput) (runtimePresetRecord, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return runtimePresetRecord{}, fmt.Errorf("%w: preset name is required", ErrRuntimeInvalid)
	}
	preset := api.RuntimePreset{ID: id.String(), Name: input.Name, Scope: input.Scope, Spec: input.Spec}
	if err := api.ValidateRuntimePreset(preset); err != nil {
		return runtimePresetRecord{}, fmt.Errorf("%w: %v", ErrRuntimeInvalid, err)
	}
	return runtimePresetRecord{
		ID: id, Name: input.Name, Description: nullableTrimmed(input.Description),
		Scope: input.Scope, Spec: input.Spec,
	}, nil
}

func runtimeWriteError(action, name string, err error) error {
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %q", ErrRuntimeNameTaken, name)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func (r runtimePresetRecord) toPreset() RuntimePreset {
	return RuntimePreset{
		ID: r.ID, Name: r.Name, Description: optionalString(r.Description), Scope: r.Scope,
		Spec: r.Spec, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
