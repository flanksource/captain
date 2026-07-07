package ai

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"rate limit", errors.New("openai: rate limit exceeded"), true},
		{"429", errors.New("genkit anthropic generate: 429 Too Many Requests"), true},
		{"503", errors.New("upstream returned 503"), true},
		{"overloaded", errors.New("anthropic: model overloaded, please retry"), true},
		{"timeout text", errors.New("request timeout after 30s"), true},
		{"ErrTimeout identity", fmt.Errorf("dispatch: %w", ErrTimeout), true},
		{"schema validation", ErrSchemaValidation, false},
		{"plain error", errors.New("invalid prompt: missing user"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryable(tc.err); got != tc.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
