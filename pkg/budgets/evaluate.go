package budgets

import (
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
)

// Attribution identifies one rule and the concrete group selected at admission.
type Attribution struct {
	RuleID      uuid.UUID
	Rule        string
	GroupValues map[string]string
	Limit       float64
	WindowStart time.Time
}

// Admission is the rule snapshot and attribution plan for one turn.
type Admission struct {
	Rules      []Rule
	Dimensions map[string]string
	Amount     float64
	byModel    map[string][]Attribution
}

// Refusal describes a budget admission failure.
type Refusal struct {
	Rule        string
	GroupValues map[string]string
	Spend       float64
	Limit       float64
	Reason      string
}

func (e *Refusal) Error() string {
	if e.Rule == "" {
		return e.Reason
	}
	return fmt.Sprintf("budget rule %q group %v refused admission: committed amount $%.8f, limit $%.8f%s",
		e.Rule, e.GroupValues, e.Spend, e.Limit, reasonSuffix(e.Reason))
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

// Evaluate derives concrete rule groups for every requested model without
// changing whether the request may proceed.
func Evaluate(rules []Rule, dimensions map[string]string, models []api.Model) *Admission {
	admission := &Admission{Rules: append([]Rule(nil), rules...), Dimensions: cloneMap(dimensions), byModel: map[string][]Attribution{}}
	for _, model := range models {
		admission.byModel[modelKey(model)] = evaluateModel(rules, dimensions, model)
	}
	return admission
}

// Enforce requires every candidate to have rule coverage and resolves the
// bucket/window inputs used by the database's atomic reservation check.
func Enforce(rules []Rule, dimensions map[string]string, models []api.Model, amount float64, now time.Time) (*Admission, error) {
	admission := &Admission{
		Rules: append([]Rule(nil), rules...), Dimensions: cloneMap(dimensions), Amount: amount,
		byModel: map[string][]Attribution{},
	}
	if len(rules) == 0 {
		return admission, nil
	}
	for _, model := range models {
		attributions, err := enforceModel(rules, dimensions, model, now)
		if err != nil {
			return nil, err
		}
		admission.byModel[modelKey(model)] = attributions
	}
	return admission, nil
}

func enforceModel(rules []Rule, dimensions map[string]string, model api.Model, now time.Time) ([]Attribution, error) {
	matched := make([]Attribution, 0)
	for _, rule := range rules {
		if !ruleMatches(rule, dimensions, model) {
			continue
		}
		group, err := groupValues(rule, dimensions)
		if err != nil {
			return nil, &Refusal{Rule: rule.Name, GroupValues: group, Limit: rule.Amount, Reason: err.Error()}
		}
		start, err := windowStart(rule.Window, now)
		if err != nil {
			return nil, &Refusal{Rule: rule.Name, GroupValues: group, Limit: rule.Amount, Reason: err.Error()}
		}
		matched = append(matched, Attribution{
			RuleID: rule.ID, Rule: rule.Name, GroupValues: group, Limit: rule.Amount, WindowStart: start,
		})
	}
	if len(matched) == 0 {
		return nil, &Refusal{Reason: fmt.Sprintf("no budget rule covers model %q and dimensions %v", model.Name, dimensions)}
	}
	return matched, nil
}

func evaluateModel(rules []Rule, dimensions map[string]string, model api.Model) []Attribution {
	matched := make([]Attribution, 0)
	for _, rule := range rules {
		if !ruleMatches(rule, dimensions, model) {
			continue
		}
		group, err := groupValues(rule, dimensions)
		if err != nil {
			continue
		}
		matched = append(matched, Attribution{RuleID: rule.ID, GroupValues: group})
	}
	return matched
}

func groupValues(rule Rule, dimensions map[string]string) (map[string]string, error) {
	group := make(map[string]string, len(rule.GroupBy))
	for _, key := range rule.GroupBy {
		value, ok := dimensions[key]
		if !ok {
			return group, fmt.Errorf("required groupBy dimension %q is missing", key)
		}
		group[key] = value
	}
	return group, nil
}

// ForModel returns the attribution selected for a candidate during admission.
func (a *Admission) ForModel(model api.Model) ([]Attribution, bool) {
	attributions, ok := a.byModel[modelKey(model)]
	if !ok {
		return nil, false
	}
	return append([]Attribution(nil), attributions...), true
}

// CheckModel resolves the admission's rule snapshot for the runtime actually
// selected by fallback. The caller atomically moves the reservation to it.
func (a *Admission) CheckModel(model api.Model, now time.Time) ([]Attribution, error) {
	if len(a.Rules) == 0 {
		return nil, nil
	}
	return enforceModel(a.Rules, a.Dimensions, model, now)
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
