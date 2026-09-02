package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type runtimeProfileRecord struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Name        string    `gorm:"column:name"`
	Description *string   `gorm:"column:description"`
	Spec        api.Spec  `gorm:"column:spec;serializer:json;type:jsonb"`
	Presets     []string  `gorm:"column:presets;serializer:json;type:jsonb"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (runtimeProfileRecord) TableName() string { return "captain_runtime_profiles" }

// RuntimeProfile is a stored profile: a task-specific spec plus the ordered
// preset references (catalog ids or names) layered beneath it.
type RuntimeProfile struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Spec        api.Spec  `json:"spec"`
	Presets     []string  `json:"presets"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type RuntimeProfileInput struct {
	Name        string
	Description string
	Spec        api.Spec
	Presets     []string
}

func (db *DB) ListRuntimeProfiles(ctx context.Context) ([]RuntimeProfile, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var records []runtimeProfileRecord
	if err := db.gorm.WithContext(ctx).Order("lower(name), id").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list captain runtime profiles: %w", err)
	}
	profiles := make([]RuntimeProfile, 0, len(records))
	for _, record := range records {
		profiles = append(profiles, record.toProfile())
	}
	return profiles, nil
}

func (db *DB) GetRuntimeProfile(ctx context.Context, id uuid.UUID) (*RuntimeProfile, error) {
	return db.findRuntimeProfile(ctx, "id = ?", id)
}

// FindRuntimeProfileByName resolves a profile by its case-insensitive name.
func (db *DB) FindRuntimeProfileByName(ctx context.Context, name string) (*RuntimeProfile, error) {
	return db.findRuntimeProfile(ctx, "lower(name) = lower(?)", strings.TrimSpace(name))
}

func (db *DB) findRuntimeProfile(ctx context.Context, query string, arg any) (*RuntimeProfile, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var record runtimeProfileRecord
	if err := db.gorm.WithContext(ctx).First(&record, query, arg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %v", ErrRuntimeProfileNotFound, arg)
		}
		return nil, fmt.Errorf("get captain runtime profile: %w", err)
	}
	profile := record.toProfile()
	return &profile, nil
}

func (db *DB) CreateRuntimeProfile(ctx context.Context, input RuntimeProfileInput) (*RuntimeProfile, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	record, err := runtimeProfileRecordFrom(uuid.New(), input)
	if err != nil {
		return nil, err
	}
	if err := db.gorm.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, runtimeWriteError("create captain runtime profile", record.Name, err)
	}
	return db.GetRuntimeProfile(ctx, record.ID)
}

// UpdateRuntimeProfile replaces every authored field and bumps updated_at.
func (db *DB) UpdateRuntimeProfile(ctx context.Context, id uuid.UUID, input RuntimeProfileInput) (*RuntimeProfile, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	record, err := runtimeProfileRecordFrom(id, input)
	if err != nil {
		return nil, err
	}
	spec, err := json.Marshal(record.Spec)
	if err != nil {
		return nil, fmt.Errorf("encode captain runtime profile spec: %w", err)
	}
	presets, err := json.Marshal(record.Presets)
	if err != nil {
		return nil, fmt.Errorf("encode captain runtime profile presets: %w", err)
	}
	result := db.gorm.WithContext(ctx).Model(&runtimeProfileRecord{}).Where("id = ?", id).Updates(map[string]any{
		"name": record.Name, "description": record.Description,
		"spec": gorm.Expr("?::jsonb", string(spec)), "presets": gorm.Expr("?::jsonb", string(presets)),
		"updated_at": clause.Expr{SQL: "now()"},
	})
	if result.Error != nil {
		return nil, runtimeWriteError("update captain runtime profile", record.Name, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: %s", ErrRuntimeProfileNotFound, id)
	}
	return db.GetRuntimeProfile(ctx, id)
}

func (db *DB) DeleteRuntimeProfile(ctx context.Context, id uuid.UUID) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	result := db.gorm.WithContext(ctx).Delete(&runtimeProfileRecord{}, "id = ?", id)
	if result.Error != nil {
		return fmt.Errorf("delete captain runtime profile: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", ErrRuntimeProfileNotFound, id)
	}
	return nil
}

// runtimeProfileRecordFrom trims the name and every preset reference. A blank
// reference is refused here because the resolver would refuse it later with a
// message naming the profile rather than the offending position.
func runtimeProfileRecordFrom(id uuid.UUID, input RuntimeProfileInput) (runtimeProfileRecord, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return runtimeProfileRecord{}, fmt.Errorf("%w: profile name is required", ErrRuntimeInvalid)
	}
	presets := make([]string, 0, len(input.Presets))
	for index, ref := range input.Presets {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return runtimeProfileRecord{}, fmt.Errorf("%w: profile %q preset reference %d is blank",
				ErrRuntimeInvalid, input.Name, index)
		}
		presets = append(presets, ref)
	}
	return runtimeProfileRecord{
		ID: id, Name: input.Name, Description: nullableTrimmed(input.Description),
		Spec: input.Spec, Presets: presets,
	}, nil
}

func (r runtimeProfileRecord) toProfile() RuntimeProfile {
	return RuntimeProfile{
		ID: r.ID, Name: r.Name, Description: optionalString(r.Description), Spec: r.Spec,
		Presets: slices.Clone(r.Presets), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
