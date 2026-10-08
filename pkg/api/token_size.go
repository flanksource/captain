package api

import (
	"context"
	"errors"
)

var ErrTokenCountingUnsupported = errors.New("provider token counting is unavailable")

type TokenCountingProvider interface {
	CountTokens(context.Context, Spec) (TokenCount, error)
}

type TokenCount struct {
	Tokens   int      `json:"tokens"`
	Excluded []string `json:"excluded,omitempty"`
}

type TokenCoverage struct {
	Partial         bool     `json:"partial"`
	FramingIncluded bool     `json:"framingIncluded"`
	Excluded        []string `json:"excluded,omitempty"`
}

// TokenSize describes content footprint, not historical billed usage.
type TokenSize struct {
	Model       string        `json:"model"`
	Source      string        `json:"source"`
	Usage       Usage         `json:"usage"`
	TotalTokens int           `json:"totalTokens"`
	CostUSD     *float64      `json:"costUSD,omitempty"`
	Coverage    TokenCoverage `json:"coverage"`
}
