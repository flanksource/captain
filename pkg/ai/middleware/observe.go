package middleware

import (
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observe"
)

func WithObservers() Option {
	return func(p ai.Provider) (ai.Provider, error) { return observe.Wrap(p), nil }
}
