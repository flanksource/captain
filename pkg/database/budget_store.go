package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	input.Match.Dimensions = cloneStrings(input.Match.Dimensions)
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

func cloneStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
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

type budgetReservationRecord struct {
	TurnID       uuid.UUID         `gorm:"column:turn_id;type:uuid;primaryKey"`
	BudgetRuleID uuid.UUID         `gorm:"column:budget_rule_id;type:uuid;primaryKey"`
	GroupValues  map[string]string `gorm:"column:group_values;serializer:json;type:jsonb;primaryKey"`
	Amount       float64           `gorm:"column:amount"`
	CreatedAt    time.Time         `gorm:"column:created_at"`
	ReleasedAt   *time.Time        `gorm:"column:released_at"`
}

func (budgetReservationRecord) TableName() string { return "captain_budget_reservations" }

// BudgetReservation is one rule/group hold requested for a turn.
type BudgetReservation struct {
	RuleID      uuid.UUID
	Rule        string
	GroupValues map[string]string
	Amount      float64
	Limit       float64
	WindowStart time.Time
}

type preparedBudgetReservation struct {
	BudgetReservation
	groupJSON []byte
	key       string
}

// BudgetCapacityError reports the atomic settled-plus-reserved capacity that
// prevented a hold from being acquired.
type BudgetCapacityError struct {
	Rule        string
	GroupValues map[string]string
	Committed   float64
	Limit       float64
}

func (e *BudgetCapacityError) Error() string {
	return fmt.Sprintf("budget rule %q group %v has $%.8f committed against a $%.8f limit",
		e.Rule, e.GroupValues, e.Committed, e.Limit)
}

// SetModelCallBudgets atomically records the turn's host dimensions and the
// concrete rule groups selected for one model call before its provider
// execution starts. Replacement is scoped to that call, so attribution already
// recorded for earlier calls in the same turn never moves.
func (db *DB) SetModelCallBudgets(ctx context.Context, turnID, modelCallID uuid.UUID, dimensions map[string]string, attributions []BudgetAttribution) error {
	dimensionJSON, err := json.Marshal(cloneStrings(dimensions))
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
		records = append(records, modelCallBudgetRecord{ModelCallID: modelCallID, BudgetRuleID: attribution.RuleID, GroupValues: cloneStrings(attribution.GroupValues)})
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

// ReserveChatTurnBudgets atomically replaces a turn's active holds. Bucket
// advisory locks serialize capacity checks across turns; sorting makes a
// multi-rule admission deadlock-safe and the transaction makes it all-or-none.
func (db *DB) ReserveChatTurnBudgets(ctx context.Context, turnID uuid.UUID, reservations []BudgetReservation) error {
	if turnID == uuid.Nil {
		return fmt.Errorf("%w: budget reservation turn ID is required", ErrBudgetInvalid)
	}
	return db.Transaction(ctx, func(tx *DB) error {
		return tx.reserveChatTurnBudgets(ctx, turnID, reservations)
	})
}

func (db *DB) reserveChatTurnBudgets(ctx context.Context, turnID uuid.UUID, reservations []BudgetReservation) error {
	prepared := make([]preparedBudgetReservation, 0, len(reservations))
	seen := make(map[string]bool, len(reservations))
	for _, reservation := range reservations {
		if reservation.RuleID == uuid.Nil || reservation.Amount <= 0 || reservation.Limit <= 0 || reservation.WindowStart.IsZero() {
			return fmt.Errorf("%w: reservation rule, positive amount/limit, and window start are required", ErrBudgetInvalid)
		}
		groupJSON, err := json.Marshal(cloneStrings(reservation.GroupValues))
		if err != nil {
			return fmt.Errorf("encode budget reservation group: %w", err)
		}
		key, err := budgetReservationKey(reservation.RuleID, reservation.GroupValues)
		if err != nil {
			return err
		}
		if seen[key] {
			return fmt.Errorf("%w: duplicate budget reservation bucket %q", ErrBudgetInvalid, key)
		}
		seen[key] = true
		prepared = append(prepared, preparedBudgetReservation{BudgetReservation: reservation, groupJSON: groupJSON, key: key})
	}

	var current []budgetReservationRecord
	if err := db.gorm.WithContext(ctx).Where("turn_id = ? AND released_at IS NULL", turnID).Find(&current).Error; err != nil {
		return fmt.Errorf("list active turn budget reservations: %w", err)
	}
	lockKeys := make([]string, 0, len(prepared)+len(current))
	lockSeen := map[string]bool{}
	for _, reservation := range prepared {
		lockKeys = append(lockKeys, reservation.key)
		lockSeen[reservation.key] = true
	}
	for _, reservation := range current {
		key, err := budgetReservationKey(reservation.BudgetRuleID, reservation.GroupValues)
		if err != nil {
			return err
		}
		if !lockSeen[key] {
			lockKeys = append(lockKeys, key)
			lockSeen[key] = true
		}
	}
	sort.Strings(lockKeys)
	for _, key := range lockKeys {
		if err := db.gorm.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
			return fmt.Errorf("lock budget reservation bucket: %w", err)
		}
	}
	if err := db.gorm.WithContext(ctx).Where("turn_id = ? AND released_at IS NULL", turnID).Find(&current).Error; err != nil {
		return fmt.Errorf("reload active turn budget reservations: %w", err)
	}
	if reservationsEqual(current, prepared) {
		return nil
	}

	for _, reservation := range prepared {
		committed, turnSpend, err := db.budgetCommittedUSD(ctx, reservation.RuleID, reservation.groupJSON, reservation.WindowStart, turnID)
		if err != nil {
			return err
		}
		turnCommitment := reservation.Amount
		if turnSpend > turnCommitment {
			turnCommitment = turnSpend
		}
		committed += turnCommitment
		if committed > reservation.Limit {
			return &BudgetCapacityError{
				Rule: reservation.Rule, GroupValues: cloneStrings(reservation.GroupValues),
				Committed: committed, Limit: reservation.Limit,
			}
		}
	}
	if err := db.gorm.WithContext(ctx).Model(&budgetReservationRecord{}).
		Where("turn_id = ? AND released_at IS NULL", turnID).Update("released_at", time.Now().UTC()).Error; err != nil {
		return fmt.Errorf("release replaced turn budget reservations: %w", err)
	}
	for _, reservation := range prepared {
		if err := db.gorm.WithContext(ctx).Exec(`
			INSERT INTO captain_budget_reservations (turn_id, budget_rule_id, group_values, amount)
			VALUES (?, ?, ?::jsonb, ?)
			ON CONFLICT (turn_id, budget_rule_id, group_values) DO UPDATE
			SET amount = EXCLUDED.amount, released_at = NULL
		`, turnID, reservation.RuleID, string(reservation.groupJSON), reservation.Amount).Error; err != nil {
			return fmt.Errorf("store turn budget reservation: %w", err)
		}
	}
	return nil
}

func reservationsEqual(current []budgetReservationRecord, desired []preparedBudgetReservation) bool {
	if len(current) != len(desired) {
		return false
	}
	want := make(map[string]float64, len(desired))
	for _, reservation := range desired {
		want[reservation.key] = reservation.Amount
	}
	for _, reservation := range current {
		key, err := budgetReservationKey(reservation.BudgetRuleID, reservation.GroupValues)
		if err != nil || want[key] != reservation.Amount {
			return false
		}
	}
	return true
}

func budgetReservationKey(ruleID uuid.UUID, groupValues map[string]string) (string, error) {
	encoded, err := json.Marshal([]any{ruleID, cloneStrings(groupValues)})
	if err != nil {
		return "", fmt.Errorf("encode budget reservation lock key: %w", err)
	}
	return string(encoded), nil
}

func (db *DB) budgetCommittedUSD(ctx context.Context, ruleID uuid.UUID, groupJSON []byte, since time.Time, exceptTurn uuid.UUID) (float64, float64, error) {
	var summary struct {
		Settled   float64
		Active    float64
		TurnSpend float64
		NonUSD    int64
	}
	if err := db.gorm.WithContext(ctx).Raw(`
		SELECT
			(SELECT COALESCE(SUM(`+modelCallCostSQL+`), 0)
			 FROM captain_model_calls calls
			 JOIN captain_model_call_budgets budgets ON budgets.model_call_id = calls.id
			 WHERE budgets.budget_rule_id = ? AND budgets.group_values = ?::jsonb
			   AND calls.ended_at IS NOT NULL AND calls.ended_at >= ? AND calls.turn_id <> ?) AS settled,
			(SELECT COALESCE(SUM(GREATEST(reservations.amount - COALESCE(costs.total, 0), 0)), 0)
			 FROM captain_budget_reservations reservations
			 LEFT JOIN LATERAL (
				 SELECT SUM(`+modelCallCostSQL+`) AS total
				 FROM captain_model_calls calls
				 JOIN captain_model_call_budgets budgets ON budgets.model_call_id = calls.id
				 WHERE calls.turn_id = reservations.turn_id
				   AND budgets.budget_rule_id = reservations.budget_rule_id
				   AND budgets.group_values = reservations.group_values
				   AND calls.ended_at IS NOT NULL
			 ) costs ON true
			 WHERE reservations.budget_rule_id = ? AND reservations.group_values = ?::jsonb
			   AND reservations.released_at IS NULL AND reservations.turn_id <> ?) AS active,
			(SELECT COALESCE(SUM(`+modelCallCostSQL+`), 0)
			 FROM captain_model_calls calls
			 JOIN captain_model_call_budgets budgets ON budgets.model_call_id = calls.id
			 WHERE calls.turn_id = ? AND budgets.budget_rule_id = ?
			   AND budgets.group_values = ?::jsonb AND calls.ended_at IS NOT NULL) AS turn_spend,
			(SELECT count(*) FROM (
				 SELECT calls.id
				 FROM captain_model_calls calls
				 JOIN captain_model_call_budgets budgets ON budgets.model_call_id = calls.id
				 WHERE budgets.budget_rule_id = ? AND budgets.group_values = ?::jsonb
				   AND calls.ended_at IS NOT NULL AND calls.ended_at >= ? AND calls.turn_id <> ?
				   AND upper(calls.currency) <> 'USD'
				 UNION
				 SELECT calls.id
				 FROM captain_budget_reservations reservations
				 JOIN captain_model_calls calls ON calls.turn_id = reservations.turn_id
				 JOIN captain_model_call_budgets budgets ON budgets.model_call_id = calls.id
				 WHERE reservations.budget_rule_id = ? AND reservations.group_values = ?::jsonb
				   AND reservations.released_at IS NULL AND reservations.turn_id <> ?
				   AND budgets.budget_rule_id = reservations.budget_rule_id
				   AND budgets.group_values = reservations.group_values
				   AND calls.ended_at IS NOT NULL AND upper(calls.currency) <> 'USD'
				 UNION
				 SELECT calls.id
				 FROM captain_model_calls calls
				 JOIN captain_model_call_budgets budgets ON budgets.model_call_id = calls.id
				 WHERE calls.turn_id = ? AND budgets.budget_rule_id = ?
				   AND budgets.group_values = ?::jsonb AND calls.ended_at IS NOT NULL
				   AND upper(calls.currency) <> 'USD'
			) non_usd_calls) AS non_usd
	`,
		ruleID, string(groupJSON), since, exceptTurn,
		ruleID, string(groupJSON), exceptTurn,
		exceptTurn, ruleID, string(groupJSON),
		ruleID, string(groupJSON), since, exceptTurn,
		ruleID, string(groupJSON), exceptTurn,
		exceptTurn, ruleID, string(groupJSON),
	).Scan(&summary).Error; err != nil {
		return 0, 0, fmt.Errorf("aggregate committed budget amount: %w", err)
	}
	if summary.NonUSD > 0 {
		return 0, 0, fmt.Errorf("budget ledger contains %d completed non-USD model calls", summary.NonUSD)
	}
	return summary.Settled + summary.Active, summary.TurnSpend, nil
}

// ReleaseChatTurnBudgetReservations releases every hold after the turn reaches
// a durable terminal state. Retained rows make retries idempotent and auditable.
func (db *DB) ReleaseChatTurnBudgetReservations(ctx context.Context, turnID uuid.UUID) error {
	if err := db.gorm.WithContext(ctx).Model(&budgetReservationRecord{}).
		Where("turn_id = ? AND released_at IS NULL", turnID).Update("released_at", time.Now().UTC()).Error; err != nil {
		return fmt.Errorf("release turn budget reservations: %w", err)
	}
	return nil
}

const modelCallCostSQL = `CASE WHEN calls.provider_cost_usd > 0 THEN calls.provider_cost_usd ELSE calls.input_cost + calls.output_cost + calls.reasoning_cost + calls.cache_read_cost + calls.cache_write_cost END`

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
	groupJSON, err := json.Marshal(cloneStrings(groupValues))
	if err != nil {
		return 0, fmt.Errorf("encode budget group: %w", err)
	}
	var nonUSD int64
	if err := db.budgetSpendStatement(ctx, ruleID, groupJSON, since).
		Where("upper(calls.currency) <> 'USD'").Count(&nonUSD).Error; err != nil {
		return 0, fmt.Errorf("inspect budget ledger currency: %w", err)
	}
	if nonUSD > 0 {
		return 0, fmt.Errorf("budget ledger contains %d completed non-USD model calls", nonUSD)
	}
	var total float64
	if err := db.budgetSpendStatement(ctx, ruleID, groupJSON, since).
		Select("COALESCE(SUM(" + modelCallCostSQL + "), 0)").
		Scan(&total).Error; err != nil {
		return 0, fmt.Errorf("aggregate completed budget spend: %w", err)
	}
	return total, nil
}
