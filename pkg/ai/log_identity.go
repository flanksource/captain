package ai

import "github.com/flanksource/captain/pkg/api"

// LogIdentity renders the same compact selector notation accepted by Captain's
// model flags: mode:model[:effort]. The effort suffix is omitted when effort is
// left at the adapter default.
//
// This lives in pkg/ai rather than pkg/ai/middleware because every layer that
// names an agent in a log line must agree on the notation the user already
// reads; middleware imports pkg/ai, so this is the lowest shared home.
func LogIdentity(mode api.RuntimeMode, model string, effort api.Effort) string {
	identity := string(mode) + ":" + model
	if effort != api.EffortNone {
		identity += ":" + string(effort)
	}
	return identity
}
