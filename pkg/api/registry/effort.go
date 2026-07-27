package registry

import "fmt"

// Effort is the per-request reasoning effort. captain owns this enum (including
// Codex's xhigh/max/ultra tiers); "" means backend default.
type Effort string

const (
	EffortNone   Effort = ""
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
	EffortUltra  Effort = "ultra"
)

// AllEfforts lists the non-empty effort tiers in ascending order.
func AllEfforts() []Effort {
	return []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortUltra}
}

// Valid reports whether e is a recognised effort tier (including none/"").
func (e Effort) Valid() bool {
	switch e {
	case EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortUltra:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown effort tier, naming the valid set.
func (e Effort) Validate() error {
	if e.Valid() {
		return nil
	}
	return fmt.Errorf("invalid reasoning effort %q; want one of: low, medium, high, xhigh, max, ultra", e)
}
