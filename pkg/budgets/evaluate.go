package budgets

import (
	"github.com/flanksource/captain/pkg/api"
)

// Attribution identifies one rule and the concrete group selected at admission.
type Attribution struct {
	RuleID      string
	GroupValues map[string]string
}

// Admission is the observe-only attribution plan for one turn.
type Admission struct {
	Dimensions map[string]string
	byModel    map[string][]Attribution
}

// Evaluate derives concrete rule groups for every requested model without
// changing whether the request may proceed.
func Evaluate(rules []Rule, dimensions map[string]string, models []api.Model) *Admission {
	admission := &Admission{Dimensions: cloneMap(dimensions), byModel: map[string][]Attribution{}}
	for _, model := range models {
		admission.byModel[modelKey(model)] = evaluateModel(rules, dimensions, model)
	}
	return admission
}

func evaluateModel(rules []Rule, dimensions map[string]string, model api.Model) []Attribution {
	matched := make([]Attribution, 0)
	for _, rule := range rules {
		if !ruleMatches(rule, dimensions, model) {
			continue
		}
		group, ok := groupValues(rule, dimensions)
		if !ok {
			continue
		}
		matched = append(matched, Attribution{RuleID: rule.ID, GroupValues: group})
	}
	return matched
}

func groupValues(rule Rule, dimensions map[string]string) (map[string]string, bool) {
	group := make(map[string]string, len(rule.GroupBy))
	for _, key := range rule.GroupBy {
		value, ok := dimensions[key]
		if !ok {
			return nil, false
		}
		group[key] = value
	}
	return group, true
}

// ForModel returns the attribution selected for a candidate during admission.
func (a *Admission) ForModel(model api.Model) ([]Attribution, bool) {
	attributions, ok := a.byModel[modelKey(model)]
	if !ok {
		return nil, false
	}
	return append([]Attribution(nil), attributions...), true
}

func modelKey(model api.Model) string {
	identity := api.RuntimeIdentityOf(model)
	return identity.Provider + "\x00" + string(identity.Mode) + "\x00" + identity.Model
}

func cloneMap(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
