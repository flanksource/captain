package budgets

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

// Attribution identifies one rule and the concrete group it meters for a turn.
type Attribution struct {
	Rule Rule

	// A rule with GroupBy keeps a separate budget for each distinct value of
	// its GroupBy keys. GroupValues holds this turn's values, which pick the
	// budget to check and charge. Empty if the rule has no GroupBy.
	// Example: GroupBy [team], turn from infra → {team: infra}.
	GroupValues map[string]string
}

// Admission is the rule snapshot and dimensions a turn was admitted under.
type Admission struct {
	// liveRules is every non-deleted rule at admission, unfiltered.
	// Attributions picks the ones that apply to a given model.
	liveRules []Rule

	Dimensions map[string]string
}

// Attributions returns the rule groups that meter model. Once any rule exists,
// a model no rule covers is refused.
func (a *Admission) Attributions(model api.Model) ([]Attribution, error) {
	if len(a.liveRules) == 0 {
		return nil, nil
	}

	var attributions []Attribution
	for _, rule := range a.liveRules {
		if !rule.Matches(a.Dimensions, model) {
			continue
		}

		group, err := rule.Group(a.Dimensions)
		if err != nil {
			return nil, &Refusal{Rule: rule.Name, GroupValues: group, Limit: rule.Amount, Reason: err.Error()}
		}
		attributions = append(attributions, Attribution{Rule: rule, GroupValues: group})
	}

	if len(attributions) == 0 {
		return nil, &Refusal{Reason: fmt.Sprintf("no budget rule covers model %q and dimensions %v", model.Name, a.Dimensions)}
	}

	return attributions, nil
}

// CanSpend returns nil when every bucket model would charge has room, a
// *Refusal when one is full or the model is uncovered, and any other error
// when spend could not be read.
func (a *Admission) CanSpend(ctx context.Context, model api.Model, now time.Time, db *database.DB) error {
	attributions, err := a.Attributions(model)
	if err != nil {
		return err
	}

	for _, attribution := range attributions {
		rule, group := attribution.Rule, attribution.GroupValues
		start, err := windowStart(rule.Window, now)
		if err != nil {
			return &Refusal{Rule: rule.Name, GroupValues: group, Limit: rule.Amount, Reason: err.Error()}
		}

		used, err := db.BudgetSpendUSD(ctx, rule.ID, group, start)
		if errors.Is(err, database.ErrBudgetSpendNotUSD) {
			return &Refusal{Rule: rule.Name, GroupValues: group, Limit: rule.Amount, Reason: err.Error()}
		}
		if err != nil {
			return fmt.Errorf("read settled spend for budget rule %q: %w", rule.Name, err)
		}

		if used >= rule.Amount {
			return &Refusal{Rule: rule.Name, GroupValues: group, Spend: used, Limit: rule.Amount}
		}
	}

	return nil
}

// Admit requires a positive per-run budget cost once any rule exists, rule
// coverage for every candidate, and every matching rule group to remain below
// its settled-spend limit.
func Admit(ctx context.Context, rules []Rule, dimensions map[string]string, models []api.Model, budget api.Budget, now time.Time, db *database.DB) (*Admission, error) {
	if len(rules) > 0 && budget.Cost <= 0 {
		return nil, &Refusal{Reason: "budget rules require a positive resolved per-run budget cost"}
	}
	admission := &Admission{liveRules: append([]Rule(nil), rules...), Dimensions: maps.Clone(dimensions)}
	for _, model := range models {
		if err := admission.CanSpend(ctx, model, now, db); err != nil {
			return nil, err
		}
	}
	return admission, nil
}
