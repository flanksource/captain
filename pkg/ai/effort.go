package ai

import "github.com/flanksource/captain/pkg/api"

// Effort is the per-request reasoning effort, re-exported from the canonical
// pkg/api enum so pkg/ai (and clicky/aichat through it) share a single source
// of truth. captain owns the "xhigh" tier.
type Effort = api.Effort

const (
	EffortNone   = api.EffortNone
	EffortLow    = api.EffortLow
	EffortMedium = api.EffortMedium
	EffortHigh   = api.EffortHigh
	EffortXHigh  = api.EffortXHigh
	EffortMax    = api.EffortMax
	EffortUltra  = api.EffortUltra
)

// AllEfforts lists the non-empty effort tiers in ascending order.
func AllEfforts() []Effort { return api.AllEfforts() }

// ValidateEffort fails loud on an unknown effort tier.
func ValidateEffort(e Effort) error { return e.Validate() }
