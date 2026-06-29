package api

// Usage is the token breakdown for one or more model calls. Canonical home;
// pkg/ai re-exports it via `type Usage = api.Usage`.
type Usage struct {
	InputTokens      int `json:"inputTokens" yaml:"inputTokens" pretty:"label=Input,table"`
	OutputTokens     int `json:"outputTokens" yaml:"outputTokens" pretty:"label=Output,table"`
	ReasoningTokens  int `json:"reasoningTokens,omitempty" yaml:"reasoningTokens,omitempty" pretty:"label=Reasoning,table"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty" yaml:"cacheReadTokens,omitempty" pretty:"label=Cache Read,table"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty" yaml:"cacheWriteTokens,omitempty" pretty:"label=Cache Write,table"`
}

// TotalTokens sums every token bucket.
func (u Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens + u.ReasoningTokens + u.CacheReadTokens + u.CacheWriteTokens
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
}

// Total is the combined cost in USD across every billable bucket.
func (c Cost) Total() float64 {
	return c.InputCost + c.OutputCost + c.ReasoningCost + c.CacheReadCost + c.CacheWriteCost
}

// Add returns the field-wise sum of two costs, keeping the receiver's Model.
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
