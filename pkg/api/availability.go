package api

// AvailabilityState is a stable machine-readable reason a runtime or model
// cannot currently be selected.
type AvailabilityState string

const (
	AvailabilityAvailable         AvailabilityState = "available"
	AvailabilityDisabled          AvailabilityState = "disabled"
	AvailabilityMissingCredential AvailabilityState = "missing_credentials"
	AvailabilityNotAuthenticated  AvailabilityState = "not_authenticated"
	AvailabilityMissingExecutable AvailabilityState = "missing_executable"
	AvailabilityMissingDependency AvailabilityState = "missing_dependency"
	AvailabilityUnsupported       AvailabilityState = "unsupported"
	AvailabilityUnavailable       AvailabilityState = "unavailable"
)

// Availability carries presentation-safe readiness details. Reason explains
// the current state; Remediation tells the user how to make it selectable.
type Availability struct {
	State       AvailabilityState `json:"state"`
	Reason      string            `json:"reason,omitempty"`
	Remediation string            `json:"remediation,omitempty"`
}

func Available() Availability {
	return Availability{State: AvailabilityAvailable}
}

func (a Availability) IsAvailable() bool {
	return a.State == AvailabilityAvailable
}
