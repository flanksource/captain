package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrBudgetRuleNotFound = errors.New("captain budget rule not found")
	ErrBudgetNameTaken    = errors.New("captain budget rule name is already taken")
	ErrBudgetInvalid      = errors.New("invalid captain budget rule")
	// ErrBudgetSpendNotUSD marks a ledger that cannot be summed in USD. It is a
	// budget decision (fail closed), not an operational failure.
	ErrBudgetSpendNotUSD = errors.New("budget ledger contains completed non-USD model calls")
)

// BudgetRuleMatch is the persisted match portion of a budget rule.
type BudgetRuleMatch struct {
	Dimensions map[string]string `json:"dimensions,omitempty"`
	Models     []string          `json:"models,omitempty"`
}

type budgetRuleRecord struct {
	ID        uuid.UUID       `gorm:"column:id;type:uuid;primaryKey"`
	Name      string          `gorm:"column:name"`
	Match     BudgetRuleMatch `gorm:"column:match;serializer:json;type:jsonb"`
	GroupBy   []string        `gorm:"column:group_by;serializer:json;type:jsonb"`
	Amount    float64         `gorm:"column:amount"`
	Window    string          `gorm:"column:window"`
	CreatedAt time.Time       `gorm:"column:created_at"`
	UpdatedAt time.Time       `gorm:"column:updated_at"`
	DeletedAt *time.Time      `gorm:"column:deleted_at"`
}

func (budgetRuleRecord) TableName() string { return "captain_budget_rules" }

// BudgetRule is one live budget rule. Deleted rules are retained for spend
// history but never returned.
type BudgetRule struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	Match     BudgetRuleMatch `json:"match"`
	GroupBy   []string        `json:"groupBy"`
	Amount    float64         `json:"amount"`
	Window    string          `json:"window"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// BudgetRuleInput contains the authored fields accepted by budget rule writes.
type BudgetRuleInput struct {
	Name    string
	Match   BudgetRuleMatch
	GroupBy []string
	Amount  float64
	Window  string
}

func (db *DB) ListBudgetRules(ctx context.Context) ([]BudgetRule, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var records []budgetRuleRecord
	if err := db.gorm.WithContext(ctx).Where("deleted_at IS NULL").Order("lower(name), id").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list captain budget rules: %w", err)
	}
	rules := make([]BudgetRule, 0, len(records))
	for _, record := range records {
		rules = append(rules, record.rule())
	}
	return rules, nil
}

func (db *DB) GetBudgetRule(ctx context.Context, id uuid.UUID) (*BudgetRule, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var record budgetRuleRecord
	if err := db.gorm.WithContext(ctx).First(&record, "id = ? AND deleted_at IS NULL", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrBudgetRuleNotFound, id)
		}
		return nil, fmt.Errorf("get captain budget rule: %w", err)
	}
	rule := record.rule()
	return &rule, nil
}

func (db *DB) CreateBudgetRule(ctx context.Context, input BudgetRuleInput) (*BudgetRule, error) {
	record, err := budgetRuleRecordFrom(uuid.New(), input)
	if err != nil {
		return nil, err
	}
	if err := db.gorm.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, budgetWriteError("create captain budget rule", record.Name, err)
	}
	return db.GetBudgetRule(ctx, record.ID)
}

func (db *DB) UpdateBudgetRule(ctx context.Context, id uuid.UUID, input BudgetRuleInput) (*BudgetRule, error) {
	record, err := budgetRuleRecordFrom(id, input)
	if err != nil {
		return nil, err
	}
	matchJSON, err := json.Marshal(record.Match)
	if err != nil {
		return nil, fmt.Errorf("encode captain budget rule match: %w", err)
	}
	groupJSON, err := json.Marshal(record.GroupBy)
	if err != nil {
		return nil, fmt.Errorf("encode captain budget rule groupBy: %w", err)
	}
	result := db.gorm.WithContext(ctx).Model(&budgetRuleRecord{}).Where("id = ? AND deleted_at IS NULL", id).Updates(map[string]any{
		"name": record.Name, "match": gorm.Expr("?::jsonb", string(matchJSON)),
		"group_by": gorm.Expr("?::jsonb", string(groupJSON)), "amount": record.Amount,
		"window": record.Window, "updated_at": clause.Expr{SQL: "now()"},
	})
	if result.Error != nil {
		return nil, budgetWriteError("update captain budget rule", record.Name, result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: %s", ErrBudgetRuleNotFound, id)
	}
	return db.GetBudgetRule(ctx, id)
}

// DeleteBudgetRule soft-deletes a live rule: admission stops matching it, while
// the model calls it already attributed keep their foreign key.
func (db *DB) DeleteBudgetRule(ctx context.Context, id uuid.UUID) error {
	result := db.gorm.WithContext(ctx).Model(&budgetRuleRecord{}).Where("id = ? AND deleted_at IS NULL", id).
		Updates(map[string]any{"deleted_at": clause.Expr{SQL: "now()"}, "updated_at": clause.Expr{SQL: "now()"}})
	if result.Error != nil {
		return fmt.Errorf("delete captain budget rule: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", ErrBudgetRuleNotFound, id)
	}
	return nil
}

func budgetRuleRecordFrom(id uuid.UUID, input BudgetRuleInput) (budgetRuleRecord, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Window = strings.TrimSpace(input.Window)
	if input.Name == "" || input.Amount <= 0 || input.Window == "" {
		return budgetRuleRecord{}, fmt.Errorf("%w: name, positive amount, and window are required", ErrBudgetInvalid)
	}
	input.Match.Dimensions = maps.Clone(input.Match.Dimensions)
	input.Match.Models = trimNonempty(input.Match.Models)
	input.GroupBy = trimNonempty(input.GroupBy)
	return budgetRuleRecord{ID: id, Name: input.Name, Match: input.Match, GroupBy: input.GroupBy, Amount: input.Amount, Window: input.Window}, nil
}

func (r budgetRuleRecord) rule() BudgetRule {
	return BudgetRule{ID: r.ID, Name: r.Name, Match: r.Match, GroupBy: r.GroupBy, Amount: r.Amount, Window: r.Window, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func budgetWriteError(action, name string, err error) error {
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %q", ErrBudgetNameTaken, name)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func trimNonempty(input []string) []string {
	out := make([]string, 0, len(input))
	for _, value := range input {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

type modelCallBudgetRecord struct {
	ModelCallID  uuid.UUID         `gorm:"column:model_call_id;type:uuid;primaryKey"`
	BudgetRuleID uuid.UUID         `gorm:"column:budget_rule_id;type:uuid;primaryKey"`
	GroupValues  map[string]string `gorm:"column:group_values;serializer:json;type:jsonb"`
}

func (modelCallBudgetRecord) TableName() string { return "captain_model_call_budgets" }

// BudgetAttribution is the concrete rule group recorded for an admitted model call.
type BudgetAttribution struct {
	RuleID      uuid.UUID
	GroupValues map[string]string
}

// SetModelCallBudgets atomically records the turn's host dimensions and the
// concrete rule groups selected for one model call before its provider
// execution starts. Replacement is scoped to that call, so attribution already
// recorded for earlier calls in the same turn never moves.
func (db *DB) SetModelCallBudgets(ctx context.Context, turnID, modelCallID uuid.UUID, dimensions map[string]string, attributions []BudgetAttribution) error {
	if dimensions == nil {
		// The column is NOT NULL DEFAULT '{}'; never store a JSON null.
		dimensions = map[string]string{}
	}
	dimensionJSON, err := json.Marshal(dimensions)
	if err != nil {
		return fmt.Errorf("encode Captain turn dimensions: %w", err)
	}
	result := db.gorm.WithContext(ctx).Model(&turnRecord{}).Where("id = ?", turnID).
		Update("dimensions", gorm.Expr("?::jsonb", string(dimensionJSON)))
	if result.Error != nil {
		return fmt.Errorf("store Captain turn dimensions: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("store Captain turn dimensions: turn %s not found", turnID)
	}
	if err := db.gorm.WithContext(ctx).Where("model_call_id = ?", modelCallID).Delete(&modelCallBudgetRecord{}).Error; err != nil {
		return fmt.Errorf("replace Captain model call budget attribution: %w", err)
	}
	records := make([]modelCallBudgetRecord, 0, len(attributions))
	for _, attribution := range attributions {
		if attribution.RuleID == uuid.Nil {
			return fmt.Errorf("%w: budget attribution rule ID is required", ErrBudgetInvalid)
		}
		records = append(records, modelCallBudgetRecord{ModelCallID: modelCallID, BudgetRuleID: attribution.RuleID, GroupValues: maps.Clone(attribution.GroupValues)})
	}
	if len(records) == 0 {
		return nil
	}
	if err := db.gorm.WithContext(ctx).Create(&records).Error; err != nil {
		return fmt.Errorf("store Captain model call budget attribution: %w", err)
	}
	return nil
}

func (db *DB) ListModelCallBudgets(ctx context.Context, modelCallID uuid.UUID) ([]BudgetAttribution, error) {
	var records []modelCallBudgetRecord
	if err := db.gorm.WithContext(ctx).Where("model_call_id = ?", modelCallID).Order("budget_rule_id").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list Captain model call budget attribution: %w", err)
	}
	out := make([]BudgetAttribution, 0, len(records))
	for _, record := range records {
		out = append(out, BudgetAttribution{RuleID: record.BudgetRuleID, GroupValues: record.GroupValues})
	}
	return out, nil
}

func (db *DB) budgetSpendStatement(ctx context.Context, ruleID uuid.UUID, groupJSON []byte, since time.Time) *gorm.DB {
	return db.gorm.WithContext(ctx).
		Table("captain_model_calls AS calls").
		Joins("JOIN captain_model_call_budgets AS budgets ON budgets.model_call_id = calls.id").
		Where("budgets.budget_rule_id = ? AND budgets.group_values = ?::jsonb", ruleID, string(groupJSON)).
		Where("calls.ended_at IS NOT NULL AND calls.ended_at >= ?", since)
}

// BudgetSpendUSD derives completed spend from the model-call ledger. Any
// non-USD completed call in the group fails closed because no conversion rate
// is authoritative here.
func (db *DB) BudgetSpendUSD(ctx context.Context, ruleID uuid.UUID, groupValues map[string]string, since time.Time) (float64, error) {
	groupJSON, err := json.Marshal(groupValues)
	if err != nil {
		return 0, fmt.Errorf("encode budget group: %w", err)
	}
	var nonUSD int64
	if err := db.budgetSpendStatement(ctx, ruleID, groupJSON, since).
		Where("upper(calls.currency) <> 'USD'").Count(&nonUSD).Error; err != nil {
		return 0, fmt.Errorf("inspect budget ledger currency: %w", err)
	}
	if nonUSD > 0 {
		return 0, fmt.Errorf("%w: %d calls", ErrBudgetSpendNotUSD, nonUSD)
	}
	var total float64
	if err := db.budgetSpendStatement(ctx, ruleID, groupJSON, since).
		Select(`COALESCE(SUM(CASE WHEN calls.provider_cost_usd > 0 THEN calls.provider_cost_usd ELSE calls.input_cost + calls.output_cost + calls.reasoning_cost + calls.cache_read_cost + calls.cache_write_cost END), 0)`).
		Scan(&total).Error; err != nil {
		return 0, fmt.Errorf("aggregate completed budget spend: %w", err)
	}
	return total, nil
}
