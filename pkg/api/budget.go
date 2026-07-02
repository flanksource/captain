package api

import "fmt"

// Budget caps a run's resource consumption. The zero value is unbounded.
type Budget struct {
	// Cost is the maximum spend in USD; 0 = no ceiling. Enforced by aborting once
	// accumulated spend would exceed it. (legacy ai.Config.BudgetUSD)
	Cost float64 `json:"cost,omitempty" yaml:"cost,omitempty" jsonschema:"minimum=0" pretty:"label=Budget USD"`

	// MaxTokens caps output tokens per model call; 0 = backend default.
	// (legacy ai.Request.MaxTokens / ai.Config.MaxTokens)
	MaxTokens int `json:"maxTokens,omitempty" yaml:"maxTokens,omitempty" jsonschema:"minimum=0" pretty:"label=Max Tokens"`

	// MaxTurns caps agent turns; 0 = backend default.
	MaxTurns int `json:"maxTurns,omitempty" yaml:"maxTurns,omitempty" jsonschema:"minimum=0,maximum=100" pretty:"label=Max Turns"`

	// Timeout caps the overall request duration. Empty means caller default.
	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty" pretty:"label=Timeout"`
}

// Validate rejects negative ceilings (fail loud).
func (b Budget) Validate() error {
	if b.Cost < 0 {
		return fmt.Errorf("invalid budget cost %v (must be >= 0)", b.Cost)
	}
	if b.MaxTokens < 0 {
		return fmt.Errorf("invalid maxTokens %d (must be >= 0)", b.MaxTokens)
	}
	if b.MaxTurns < 0 || b.MaxTurns > 100 {
		return fmt.Errorf("invalid maxTurns %d (valid: 0-100, 0=backend default)", b.MaxTurns)
	}
	return nil
}
