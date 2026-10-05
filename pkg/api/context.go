package api

// ContextUsage is the provider's latest context occupancy, independent of billing usage.
// A nil snapshot means the provider has not reported context information.
type ContextUsage struct {
	UsedTokens   int `json:"usedTokens"`
	WindowTokens int `json:"windowTokens"`
	FreePercent  int `json:"freePercent"`
}
