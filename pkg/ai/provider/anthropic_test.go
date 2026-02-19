package provider

import (
	"context"
	"os"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/stretchr/testify/require"
)

func TestAnthropicDefaults(t *testing.T) {
	p := NewAnthropic("", "key")
	require.Equal(t, "claude-sonnet-4", p.GetModel())
	require.Equal(t, ai.BackendAnthropic, p.GetBackend())

	p2 := NewAnthropic("claude-opus-4", "key")
	require.Equal(t, "claude-opus-4", p2.GetModel())
}

func TestAnthropicNoAPIKey(t *testing.T) {
	p := NewAnthropic("claude-sonnet-4", "")
	_, err := p.Execute(context.Background(), ai.Request{Prompt: "hello"})
	require.ErrorIs(t, err, ai.ErrNoAPIKey)
}

func TestAnthropicIntegration(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	p := NewAnthropic("", apiKey)
	resp, err := p.Execute(context.Background(), ai.Request{
		Prompt:    "Reply with exactly: hello",
		MaxTokens: 32,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Text)
	require.Equal(t, ai.BackendAnthropic, resp.Backend)
	require.Greater(t, resp.Usage.InputTokens, 0)
	require.Greater(t, resp.Usage.OutputTokens, 0)
}

func TestAnthropicStructuredIntegration(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	type Capital struct {
		City    string `json:"city"`
		Country string `json:"country"`
	}

	p := NewAnthropic("", apiKey)
	var result Capital
	resp, err := p.Execute(context.Background(), ai.Request{
		Prompt:           "What is the capital of France? Return city and country.",
		MaxTokens:        128,
		StructuredOutput: &result,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Text)
	require.NotNil(t, resp.StructuredData)
	require.Equal(t, "Paris", result.City)
	require.Equal(t, "France", result.Country)
}
