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

func TestIsFallbackEligible(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"retryable", errors.New("429 rate limit"), true},
		{"typed unavailable", fmt.Errorf("codex: %w", ErrModelUnavailable), true},
		{"codex chatgpt unsupported", errors.New("The 'gpt-5.6-line' model is not supported when using Codex with a ChatGPT account."), true},
		{"unknown model", errors.New("provider rejected unknown model gpt-future"), true},
		{"invalid model name", errors.New("invalid model name gpt-future"), true},
		{"typed missing key", fmt.Errorf("openai: %w", ErrNoAPIKey), true},
		{"missing key text", errors.New("genkit provider: no API key for backend openai"), true},
		{"missing cli", fmt.Errorf("start codex: %w", ErrCLINotFound), true},
		{"bad key remains terminal", errors.New("authentication failed: invalid API key"), false},
		{"pricing miss remains terminal", fmt.Errorf("price lookup: %w", ErrModelNotFound), false},
		{"reasoning effort remains terminal", errors.New("model gpt-5.5 does not support reasoning effort max"), false},
		{"malformed request", errors.New("invalid request: missing user"), false},
		{"schema validation", ErrSchemaValidation, false},
		{"invalid model output", errors.New("invalid model output: missing body"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFallbackEligible(tc.err); got != tc.want {
				t.Errorf("IsFallbackEligible(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
