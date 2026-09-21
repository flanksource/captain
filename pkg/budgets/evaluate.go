package budgets

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// SpendReader derives completed USD spend for a concrete attributed group.
type SpendReader interface {
	BudgetSpendUSD(context.Context, string, map[string]string, time.Time) (float64, error)
}

// Attribution identifies one rule and the concrete group selected at admission.
type Attribution struct {
	RuleID      string
	GroupValues map[string]string
}

// Admission is the immutable rule snapshot and attribution plan for one turn.
type Admission struct {
	Rules      []Rule
	Dimensions map[string]string
	byModel    map[string][]Attribution
	recorded   []Attribution
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
	return fmt.Sprintf("budget rule %q group %v refused admission: completed spend $%.8f, limit $%.8f%s",
		e.Rule, e.GroupValues, e.Spend, e.Limit, reasonSuffix(e.Reason))
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

// Evaluate checks every requested candidate. This intentionally prevents a
// configured fallback from becoming an unbudgeted escape hatch.
func Evaluate(ctx context.Context, rules []Rule, dimensions map[string]string, models []api.Model, now time.Time, spend SpendReader) (*Admission, error) {
	admission := &Admission{Rules: append([]Rule(nil), rules...), Dimensions: cloneMap(dimensions), byModel: map[string][]Attribution{}}
	if len(rules) == 0 {
		return admission, nil
	}
	for _, model := range models {
		attributions, err := evaluateModel(ctx, rules, dimensions, model, now, spend)
		if err != nil {
			return nil, err
		}
		admission.byModel[modelKey(model)] = attributions
	}
	return admission, nil
}

func evaluateModel(ctx context.Context, rules []Rule, dimensions map[string]string, model api.Model, now time.Time, spend SpendReader) ([]Attribution, error) {
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
		used, err := spend.BudgetSpendUSD(ctx, rule.ID, group, start)
		if err != nil {
			return nil, &Refusal{Rule: rule.Name, GroupValues: group, Limit: rule.Amount, Reason: err.Error()}
		}
		if used >= rule.Amount {
			return nil, &Refusal{Rule: rule.Name, GroupValues: group, Spend: used, Limit: rule.Amount}
		}
		matched = append(matched, Attribution{RuleID: rule.ID, GroupValues: group})
	}
	if len(matched) == 0 {
		return nil, &Refusal{Reason: fmt.Sprintf("no budget rule covers model %q and dimensions %v", model.Name, dimensions)}
	}
	return matched, nil
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

// CheckModel repeats the completed-spend check against the admission's rule
// snapshot for the runtime actually selected by fallback.
func (a *Admission) CheckModel(ctx context.Context, model api.Model, now time.Time, spend SpendReader) ([]Attribution, error) {
	if len(a.Rules) == 0 {
		return append([]Attribution(nil), a.recorded...), nil
	}
	if a.recorded != nil {
		if err := CheckRecorded(ctx, a.Rules, a.recorded, now, spend); err != nil {
			return nil, err
		}
		return append([]Attribution(nil), a.recorded...), nil
	}
	return evaluateModel(ctx, a.Rules, a.Dimensions, model, now, spend)
}

// CheckRecorded rechecks immutable turn attribution during an approval resume.
// Match and grouping are not rerun, so edits affect future turns only.
func CheckRecorded(ctx context.Context, rules []Rule, recorded []Attribution, now time.Time, spend SpendReader) error {
	if len(rules) == 0 {
		return nil
	}
	if len(recorded) == 0 {
		return &Refusal{Reason: "the turn has no recorded budget attribution"}
	}
	byID := make(map[string]Rule, len(rules))
	for _, rule := range rules {
		byID[rule.ID] = rule
	}
	for _, attribution := range recorded {
		rule, ok := byID[attribution.RuleID]
		if !ok {
			return &Refusal{Reason: fmt.Sprintf("recorded budget rule %q is no longer available", attribution.RuleID)}
		}
		start, err := windowStart(rule.Window, now)
		if err != nil {
			return &Refusal{Rule: rule.Name, GroupValues: attribution.GroupValues, Limit: rule.Amount, Reason: err.Error()}
		}
		used, err := spend.BudgetSpendUSD(ctx, rule.ID, attribution.GroupValues, start)
		if err != nil {
			return &Refusal{Rule: rule.Name, GroupValues: attribution.GroupValues, Limit: rule.Amount, Reason: err.Error()}
		}
		if used >= rule.Amount {
			return &Refusal{Rule: rule.Name, GroupValues: attribution.GroupValues, Spend: used, Limit: rule.Amount}
		}
	}
	return nil
}

// AdmissionFromRecorded reconstructs the immutable rule set used by a
// suspended turn so its eventual fallback binding is checked against the same
// attribution rather than today's unrelated rules.
func AdmissionFromRecorded(rules []Rule, recorded []Attribution, dimensions map[string]string) (*Admission, error) {
	if len(rules) == 0 {
		return &Admission{Dimensions: cloneMap(dimensions), byModel: map[string][]Attribution{}, recorded: append([]Attribution(nil), recorded...)}, nil
	}
	byID := make(map[string]Rule, len(rules))
	for _, rule := range rules {
		byID[rule.ID] = rule
	}
	selected := make([]Rule, 0, len(recorded))
	for _, attribution := range recorded {
		rule, ok := byID[attribution.RuleID]
		if !ok {
			return nil, &Refusal{Reason: fmt.Sprintf("recorded budget rule %q is no longer available", attribution.RuleID)}
		}
		selected = append(selected, rule)
	}
	return &Admission{Rules: selected, Dimensions: cloneMap(dimensions), byModel: map[string][]Attribution{}, recorded: append([]Attribution(nil), recorded...)}, nil
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
