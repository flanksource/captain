package budgets

import (
	"fmt"
	"maps"

	"github.com/flanksource/captain/pkg/api"
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

	// Amount is the per-run hold reserved in every attributed bucket: the
	// resolved per-run budget cost.
	Amount float64
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

// Admit requires a positive per-run budget cost once any rule exists and rule
// coverage for every candidate. Whether a bucket has room is decided when the
// turn reserves its hold, atomically with every other turn.
func Admit(rules []Rule, dimensions map[string]string, models []api.Model, budget api.Budget) (*Admission, error) {
	if len(rules) > 0 && budget.Cost <= 0 {
		return nil, &Refusal{Reason: "budget rules require a positive resolved per-run budget cost"}
	}
	admission := &Admission{liveRules: append([]Rule(nil), rules...), Dimensions: maps.Clone(dimensions), Amount: budget.Cost}
	for _, model := range models {
		if _, err := admission.Attributions(model); err != nil {
			return nil, err
		}
	}
	return admission, nil
}
