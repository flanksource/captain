package api

// Usage is the token breakdown for one or more model calls. Canonical home;
// pkg/ai re-exports it via `type Usage = api.Usage`.
//
// The buckets are DISJOINT and additive: InputTokens excludes cache reads,
// OutputTokens excludes reasoning, and TotalTokens is their plain sum. Providers
// whose wire format reports overlapping counts (OpenAI/Codex fold cache into
// prompt tokens and reasoning into completion tokens; Gemini folds cache into
// prompt tokens) MUST normalize at their parse boundary via NetInputTokens /
// NetOutputTokens before populating this struct, so cost pricing and totals do
// not double-count. Anthropic already reports disjoint buckets natively.
type Usage struct {
	InputTokens      int `json:"inputTokens" yaml:"inputTokens" pretty:"label=Input,table"`
	OutputTokens     int `json:"outputTokens" yaml:"outputTokens" pretty:"label=Output,table"`
	ReasoningTokens  int `json:"reasoningTokens,omitempty" yaml:"reasoningTokens,omitempty" pretty:"label=Reasoning,table"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty" yaml:"cacheReadTokens,omitempty" pretty:"label=Cache Read,table"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty" yaml:"cacheWriteTokens,omitempty" pretty:"label=Cache Write,table"`
}

// TotalTokens sums every token bucket. Correct only under the disjoint-bucket
// invariant documented on Usage.
func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens + u.ReasoningTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// NetInputTokens returns input tokens with the cached prompt subset removed, per
// the disjoint-bucket contract (InputTokens must exclude cache reads). If the
// cached count is absent or inconsistent (exceeds input), the input is returned
// unchanged rather than clamped to zero, so malformed provider data cannot erase
// real input tokens.
func NetInputTokens(input, cached int) int {
	if cached <= 0 || input-cached < 0 {
		return input
	}
	return input - cached
}

// NetOutputTokens returns output tokens with the reasoning subset removed, per
// the disjoint-bucket contract (OutputTokens must exclude reasoning). Uses the
// same inconsistency guard as NetInputTokens.
func NetOutputTokens(output, reasoning int) int {
	if reasoning <= 0 || output-reasoning < 0 {
		return output
	}
	return output - reasoning
}

// Cost is the token + money accounting for a model call. Canonical home; pkg/ai
// re-exports it via `type Cost = api.Cost`.
type Cost struct {
	Model            string  `json:"model,omitempty" yaml:"model,omitempty" pretty:"label=Model,table"`
	InputTokens      int     `json:"inputTokens" yaml:"inputTokens" pretty:"label=Input,table"`
	OutputTokens     int     `json:"outputTokens" yaml:"outputTokens" pretty:"label=Output,table"`
	ReasoningTokens  int     `json:"reasoningTokens,omitempty" yaml:"reasoningTokens,omitempty" pretty:"label=Reasoning,table"`
	CacheReadTokens  int     `json:"cacheReadTokens,omitempty" yaml:"cacheReadTokens,omitempty" pretty:"label=Cache Read,table"`
	CacheWriteTokens int     `json:"cacheWriteTokens,omitempty" yaml:"cacheWriteTokens,omitempty" pretty:"label=Cache Write,table"`
	TotalTokens      int     `json:"totalTokens" yaml:"totalTokens" pretty:"label=Total,table"`
	InputCost        float64 `json:"inputCost" yaml:"inputCost" pretty:"label=Input $,table"`
	OutputCost       float64 `json:"outputCost" yaml:"outputCost" pretty:"label=Output $,table"`
	ReasoningCost    float64 `json:"reasoningCost,omitempty" yaml:"reasoningCost,omitempty" pretty:"label=Reasoning $,table"`
	CacheReadCost    float64 `json:"cacheReadCost,omitempty" yaml:"cacheReadCost,omitempty" pretty:"label=Cache Read $,table"`
	CacheWriteCost   float64 `json:"cacheWriteCost,omitempty" yaml:"cacheWriteCost,omitempty" pretty:"label=Cache Write $,table"`

	// ProviderCostUSD, when > 0, is the authoritative total the model provider
	// reported for this call (e.g. the claude CLI's total_cost_usd). Total() returns it
	// in preference to the list-price bucket sum, so provider billing wins over a
	// recomputed estimate. The per-bucket costs are retained for display; this
	// value is not rendered as its own column since Total already reflects it.
	ProviderCostUSD float64 `json:"providerCostUSD,omitempty" yaml:"providerCostUSD,omitempty"`
}

// Total is the combined cost in USD. It prefers the provider-reported total when
// present, falling back to the sum of the list-price buckets.
func (c Cost) Total() float64 {
	if c.ProviderCostUSD > 0 {
		return c.ProviderCostUSD
	}
	return c.InputCost + c.OutputCost + c.ReasoningCost + c.CacheReadCost + c.CacheWriteCost
}

// Add returns the field-wise sum of two costs, keeping the receiver's Model. The
// provider-reported totals sum too, so a rollup's Total() stays authoritative
// across a session (which, in practice, uses a single provider throughout).
func (c Cost) Add(other Cost) Cost {
	return Cost{
		Model:            c.Model,
		InputTokens:      c.InputTokens + other.InputTokens,
		OutputTokens:     c.OutputTokens + other.OutputTokens,
		ReasoningTokens:  c.ReasoningTokens + other.ReasoningTokens,
		CacheReadTokens:  c.CacheReadTokens + other.CacheReadTokens,
		CacheWriteTokens: c.CacheWriteTokens + other.CacheWriteTokens,
		TotalTokens:      c.TotalTokens + other.TotalTokens,
		InputCost:        c.InputCost + other.InputCost,
		OutputCost:       c.OutputCost + other.OutputCost,
		ReasoningCost:    c.ReasoningCost + other.ReasoningCost,
		CacheReadCost:    c.CacheReadCost + other.CacheReadCost,
		CacheWriteCost:   c.CacheWriteCost + other.CacheWriteCost,
		ProviderCostUSD:  c.ProviderCostUSD + other.ProviderCostUSD,
	}
}

// Costs is a list of per-call costs.
type Costs []Cost

// Sum collapses all entries into a single Cost.
func (c Costs) Sum() Cost {
	var total Cost
	for _, cost := range c {
		total = total.Add(cost)
	}
	return total
}

// ByModel groups and sums the costs keyed by model name.
func (c Costs) ByModel() map[string]Cost {
	m := make(map[string]Cost)
	for _, cost := range c {
		m[cost.Model] = m[cost.Model].Add(cost)
	}
	return m
}
