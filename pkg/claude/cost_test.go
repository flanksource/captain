package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyModel(t *testing.T) {
	tests := []struct {
		model    string
		expected ModelFamily
	}{
		{"claude-opus-4-6", ModelFamilyOpus4},
		{"claude-opus-4-5-20251101", ModelFamilyOpus4},
		{"claude-sonnet-4-6", ModelFamilySonnet4},
		{"claude-sonnet-4-5-20241022", ModelFamilySonnet4},
		{"claude-haiku-4-5-20251001", ModelFamilyHaiku4},
		{"", ModelFamilyUnknown},
		{"gpt-4o", ModelFamilyUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.expected, ClassifyModel(tt.model))
		})
	}
}

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name     string
		usage    *Usage
		model    string
		expected float64
	}{
		{
			name:     "nil usage",
			usage:    nil,
			model:    "claude-opus-4-6",
			expected: 0,
		},
		{
			name: "opus 1M input + 1M output",
			usage: &Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			model:    "claude-opus-4-6",
			expected: 15.0 + 75.0, // $90
		},
		{
			name: "sonnet with cache",
			usage: &Usage{
				InputTokens:              500_000,
				OutputTokens:             100_000,
				CacheCreationInputTokens: 200_000,
				CacheReadInputTokens:     300_000,
			},
			model: "claude-sonnet-4-6",
			// 0.5M * 3.0 + 0.1M * 15.0 + 0.2M * 3.75 + 0.3M * 0.30
			expected: 1.5 + 1.5 + 0.75 + 0.09,
		},
		{
			name: "haiku basic",
			usage: &Usage{
				InputTokens:  100_000,
				OutputTokens: 50_000,
			},
			model:    "claude-haiku-4-5-20251001",
			expected: 0.08 + 0.20, // $0.28
		},
		{
			name: "unknown model falls back to sonnet pricing",
			usage: &Usage{
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			model:    "unknown-model",
			expected: 3.0 + 15.0, // $18
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateCost(tt.usage, tt.model)
			assert.InDelta(t, tt.expected, got, 0.001)
		})
	}
}

func TestTokenSummary_Add(t *testing.T) {
	var s TokenSummary

	s.Add(&Usage{
		InputTokens:              100,
		OutputTokens:             200,
		CacheCreationInputTokens: 50,
		CacheReadInputTokens:     30,
	}, "claude-sonnet-4-6")

	s.Add(&Usage{
		InputTokens:  400,
		OutputTokens: 100,
	}, "claude-sonnet-4-6")

	assert.Equal(t, 500, s.InputTokens)
	assert.Equal(t, 300, s.OutputTokens)
	assert.Equal(t, 50, s.CacheWriteTokens)
	assert.Equal(t, 30, s.CacheReadTokens)
	assert.Equal(t, 880, s.TotalTokens())
	assert.Greater(t, s.TotalCost, 0.0)

	// nil usage should be a no-op
	s.Add(nil, "claude-sonnet-4-6")
	assert.Equal(t, 500, s.InputTokens)
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"hi", 1},           // 2 chars → ceil(2/4) = 1
		{"hello", 2},        // 5 chars → ceil(5/4) = 2
		{"12345678", 2},     // 8 chars → exactly 2
		{"123456789", 3},    // 9 chars → ceil(9/4) = 3
		{"hello world foo", 4}, // 15 chars → ceil(15/4) = 4
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, EstimateTokens(tt.input))
		})
	}
}

func TestEstimateContentTokens(t *testing.T) {
	assert.Equal(t, 0, EstimateContentTokens(nil))
	assert.Equal(t, 0, EstimateContentTokens(json.RawMessage{}))
	assert.Greater(t, EstimateContentTokens(json.RawMessage(`{"file_path":"/tmp/foo.go"}`)), 0)
}

func TestAggregateByTool(t *testing.T) {
	toolUses := []ToolUse{
		{Tool: "Bash", InputTokens: 100, OutputTokens: 500},
		{Tool: "Bash", InputTokens: 50, OutputTokens: 200, IsError: true},
		{Tool: "Read", InputTokens: 20, OutputTokens: 1000},
		{Tool: "Edit", InputTokens: 80, OutputTokens: 30},
	}

	result := AggregateByTool(toolUses)

	assert.Len(t, result, 3)
	// Sorted by total tokens descending: Read (1020), Bash (850), Edit (110)
	assert.Equal(t, "Read", result[0].Tool)
	assert.Equal(t, 1, result[0].CallCount)
	assert.Equal(t, 1020, result[0].TotalTokens())

	assert.Equal(t, "Bash", result[1].Tool)
	assert.Equal(t, 2, result[1].CallCount)
	assert.Equal(t, 1, result[1].ErrorCount)
	assert.Equal(t, 150, result[1].InputTokens)
	assert.Equal(t, 700, result[1].OutputTokens)

	assert.Equal(t, "Edit", result[2].Tool)
}
