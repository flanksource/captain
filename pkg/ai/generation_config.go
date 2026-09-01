package ai

// EffortConfig builds the provider-native generation config for one request.
// The per-provider translation lives on the provider descriptor
// (registry.Provider.GenerationConfig), so it sits next to the capability data
// that gates it rather than in a per-runtime switch here.
//
// Returns nil when there is nothing to send.
func EffortConfig(p *ModelProvider, mode RuntimeMode, model string, effort Effort, maxTokens int, temperature *float64) map[string]any {
	if p == nil {
		return nil
	}
	return p.GenerationConfig(mode, model, effort, maxTokens, temperature)
}
