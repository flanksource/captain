package ai

import (
	"reflect"
	"testing"
)

func f64(v float64) *float64 { return &v }

func TestEffortConfig(t *testing.T) {
	cases := []struct {
		name        string
		backend     Backend
		model       string
		effort      Effort
		maxTokens   int
		temperature *float64
		want        map[string]any
	}{
		{
			name:    "anthropic no effort emits max_tokens only",
			backend: BackendAnthropic, model: "claude-sonnet-5", effort: EffortNone,
			want: map[string]any{"max_tokens": 4096},
		},
		{
			name:    "anthropic enabled model uses budget_tokens schema",
			backend: BackendAnthropic, model: "claude-sonnet-4-6", effort: EffortHigh,
			want: map[string]any{
				"max_tokens": 24576 + 4096,
				"thinking":   map[string]any{"type": "enabled", "budget_tokens": 24576},
			},
		},
		{
			name:    "anthropic adaptive model uses output_config schema",
			backend: BackendAnthropic, model: "claude-sonnet-5", effort: EffortHigh,
			want: map[string]any{
				"max_tokens":    24576 + 4096,
				"thinking":      map[string]any{"type": "adaptive"},
				"output_config": map[string]any{"effort": "high"},
			},
		},
		{
			// Regression: opus-4-8 must use adaptive, not enabled, or the API 400s.
			name:    "anthropic opus-4-8 is adaptive",
			backend: BackendAnthropic, model: "claude-opus-4-8", effort: EffortHigh,
			want: map[string]any{
				"max_tokens":    24576 + 4096,
				"thinking":      map[string]any{"type": "adaptive"},
				"output_config": map[string]any{"effort": "high"},
			},
		},
		{
			name:    "anthropic xhigh clamps output effort to high",
			backend: BackendAnthropic, model: "anthropic/claude-fable-5", effort: EffortXHigh,
			want: map[string]any{
				"max_tokens":    32768 + 4096,
				"thinking":      map[string]any{"type": "adaptive"},
				"output_config": map[string]any{"effort": "high"},
			},
		},
		{
			name:    "anthropic honours explicit max tokens as base",
			backend: BackendAnthropic, model: "claude-sonnet-4-6", effort: EffortMedium, maxTokens: 1000,
			want: map[string]any{
				"max_tokens": 8192 + 1000,
				"thinking":   map[string]any{"type": "enabled", "budget_tokens": 8192},
			},
		},
		{
			name:    "anthropic unknown model omits thinking (no reasoning capability)",
			backend: BackendAnthropic, model: "claude-3-5-sonnet-20241022", effort: EffortHigh,
			want: map[string]any{"max_tokens": 4096},
		},
		{
			name:    "openai reasoning model sets reasoning_effort",
			backend: BackendOpenAI, model: "gpt-5.5", effort: EffortHigh,
			want: map[string]any{"reasoning_effort": "high"},
		},
		{
			name:    "openai no effort omits config",
			backend: BackendOpenAI, model: "gpt-5.5", effort: EffortNone,
			want: nil,
		},
		{
			name:    "gemini low effort sets thinkingBudget",
			backend: BackendGemini, model: "gemini-3.5-flash", effort: EffortLow,
			want: map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 2048}},
		},
		{
			name:    "deepseek omits effort config",
			backend: BackendDeepSeek, model: "deepseek-v4-pro", effort: EffortHigh,
			want: nil,
		},
		{
			name:    "temperature included for a temperature-capable model",
			backend: BackendGemini, model: "gemini-3.5-flash", effort: EffortNone, temperature: f64(0.5),
			want: map[string]any{"temperature": 0.5},
		},
		{
			name:    "temperature dropped for a temperature-incapable model",
			backend: BackendAnthropic, model: "claude-sonnet-5", effort: EffortNone, temperature: f64(0.7),
			want: map[string]any{"max_tokens": 4096},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EffortConfig(tc.backend, tc.model, tc.effort, tc.maxTokens, tc.temperature)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("EffortConfig() = %#v, want %#v", got, tc.want)
			}
		})
	}
}
