package ai

import "github.com/flanksource/captain/pkg/api/registry"

// EffortConfig builds the provider-native generation config for one request.
// The per-provider translation lives on the provider descriptor
// (registry.Provider.GenerationConfig), so it sits next to the capability data
// that gates it rather than in a backend switch here.
//
// Returns nil when there is nothing to send.
func EffortConfig(backend Backend, model string, effort Effort, maxTokens int, temperature *float64) map[string]any {
	p, mode, ok := registry.ProviderFor(backend)
	if !ok {
		return nil
	}
	return p.GenerationConfig(mode, model, effort, maxTokens, temperature)
}
