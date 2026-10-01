package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/budgets"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
)

const budgetToolGroup = "captain.budget"

var registerBudgetEntitiesOnce sync.Once

// RegisterBudgetEntities publishes the budget-rule entity on every clicky
// surface: CLI, /api/v1, OpenAPI and MCP.
func RegisterBudgetEntities() {
	registerBudgetEntitiesOnce.Do(registerBudgetRuleEntity)
}

type BudgetRuleListOptions struct {
	Query string `flag:"query" help:"Search rule name"`
}

// BudgetRuleRecord is a live budget rule as the entity lists and returns it.
type BudgetRuleRecord struct {
	budgets.Rule
}

func (r BudgetRuleRecord) GetID() string   { return r.ID.String() }
func (r BudgetRuleRecord) GetName() string { return r.Name }

func (r BudgetRuleRecord) Columns() []clickyapi.ColumnDef {
	return []clickyapi.ColumnDef{
		clickyapi.Column("name").Label("Name").Build(),
		clickyapi.Column("match").Label("Match").MaxWidth(60).Build(),
		clickyapi.Column("groupBy").Label("Group by").Build(),
		clickyapi.Column("amount").Label("Amount (USD)").Build(),
		clickyapi.Column("window").Label("Window").Build(),
	}
}

func (r BudgetRuleRecord) Row() map[string]any {
	return map[string]any{
		"name":    r.Name,
		"match":   budgetMatchLabel(r.Match),
		"groupBy": strings.Join(r.GroupBy, ", "),
		"amount":  fmt.Sprintf("%.2f", r.Amount),
		"window":  r.Window,
	}
}

// BudgetRuleWriteRequest is the create/update body. ID is only accepted on
// update, where clicky carries it in the body of the collection PUT.
type BudgetRuleWriteRequest struct {
	ID      string            `json:"id,omitempty"`
	Name    string            `json:"name"`
	Match   budgets.RuleMatch `json:"match,omitempty"`
	GroupBy []string          `json:"groupBy,omitempty"`
	Amount  float64           `json:"amount"`
	Window  string            `json:"window"`
}

func (req BudgetRuleWriteRequest) input() budgets.RuleInput {
	return budgets.RuleInput{Name: req.Name, Match: req.Match, GroupBy: req.GroupBy, Amount: req.Amount, Window: req.Window}
}

func registerBudgetRuleEntity() {
	clicky.NewEntity[BudgetRuleRecord, BudgetRuleListOptions, BudgetRuleRecord]("budget-rule").
		Aliases("budget-rules").
		ToolGroup(budgetToolGroup).
		ListWithContext(listBudgetRules).
		GetWithContext(getBudgetRule).
		CreateWithContext(createBudgetRule).
		UpdateWithContext(updateBudgetRule).
		DeleteWithContext(deleteBudgetRule).
		Register()
}

func buildBudgetCatalog() (*budgets.Catalog, error) {
	return budgets.NewCatalog(budgets.CatalogOptions{Read: captainDB, Write: captainDefaultDB})
}

func listBudgetRules(ctx context.Context, opts BudgetRuleListOptions) ([]BudgetRuleRecord, error) {
	catalog, err := buildBudgetCatalog()
	if err != nil {
		return nil, err
	}
	rules, err := catalog.List(ctx)
	if err != nil {
		return nil, budgetCatalogError(err)
	}
	out := []BudgetRuleRecord{}
	for _, rule := range rules {
		if runtimeQueryMatches(opts.Query, rule.Name) {
			out = append(out, BudgetRuleRecord{Rule: rule})
		}
	}
	return out, nil
}

func getBudgetRule(ctx context.Context, id string) (BudgetRuleRecord, error) {
	catalog, err := buildBudgetCatalog()
	if err != nil {
		return BudgetRuleRecord{}, err
	}
	rule, err := catalog.Get(ctx, id)
	if err != nil {
		return BudgetRuleRecord{}, budgetCatalogError(err)
	}
	return BudgetRuleRecord{Rule: rule}, nil
}

func createBudgetRule(ctx context.Context, body map[string]any) (BudgetRuleRecord, error) {
	var req BudgetRuleWriteRequest
	if err := decodeRuntimeBody(ctx, body, &req); err != nil {
		return BudgetRuleRecord{}, err
	}
	if err := requireNoRuntimeID(req.ID); err != nil {
		return BudgetRuleRecord{}, err
	}
	catalog, err := buildBudgetCatalog()
	if err != nil {
		return BudgetRuleRecord{}, err
	}
	rule, err := catalog.Create(ctx, req.input())
	if err != nil {
		return BudgetRuleRecord{}, budgetCatalogError(err)
	}
	return BudgetRuleRecord{Rule: rule}, nil
}

func updateBudgetRule(ctx context.Context, id string, body map[string]any) (BudgetRuleRecord, error) {
	var req BudgetRuleWriteRequest
	if err := decodeRuntimeBody(ctx, body, &req); err != nil {
		return BudgetRuleRecord{}, err
	}
	if err := requireRuntimeIDMatch(req.ID, id); err != nil {
		return BudgetRuleRecord{}, err
	}
	catalog, err := buildBudgetCatalog()
	if err != nil {
		return BudgetRuleRecord{}, err
	}
	rule, err := catalog.Update(ctx, id, req.input())
	if err != nil {
		return BudgetRuleRecord{}, budgetCatalogError(err)
	}
	return BudgetRuleRecord{Rule: rule}, nil
}

func deleteBudgetRule(ctx context.Context, id string) error {
	catalog, err := buildBudgetCatalog()
	if err != nil {
		return err
	}
	return budgetCatalogError(catalog.Delete(ctx, id))
}

// budgetMatchLabel renders a match as `key=pattern` pairs followed by model
// selectors, or "all requests" when the rule matches everything.
func budgetMatchLabel(match budgets.RuleMatch) string {
	parts := make([]string, 0, len(match.Dimensions)+1)
	for key, pattern := range match.Dimensions {
		parts = append(parts, key+"="+pattern)
	}
	slices.Sort(parts)
	if len(match.Models) > 0 {
		parts = append(parts, "models: "+strings.Join(match.Models, ", "))
	}
	if len(parts) == 0 {
		return "all requests"
	}
	return strings.Join(parts, "; ")
}

// budgetCatalogError maps catalog failures onto HTTP statuses for the entity
// surface; anything unrecognised stays an internal error.
func budgetCatalogError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, budgets.ErrNotFound):
		return entity.NewStatusErrorf(http.StatusNotFound, "not_found", "%v", err)
	case errors.Is(err, budgets.ErrNameTaken):
		return entity.NewStatusErrorf(http.StatusConflict, "name_taken", "%v", err)
	case errors.Is(err, budgets.ErrReadOnly):
		return entity.NewStatusErrorf(http.StatusConflict, "read_only", "%v", err)
	case errors.Is(err, budgets.ErrInvalid):
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid", "%v", err)
	default:
		return err
	}
}
