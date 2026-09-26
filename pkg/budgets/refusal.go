package budgets

import "fmt"

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
	msg := fmt.Sprintf("budget rule %q group %v refused admission: settled spend $%.8f, limit $%.8f",
		e.Rule, e.GroupValues, e.Spend, e.Limit)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}
