package provider

import (
	"context"
	"os"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/stretchr/testify/require"
)

func TestGeminiDefaults(t *testing.T) {
	p := NewGemini(ai.Config{APIKey: "key"})
	require.Equal(t, "gemini-2.0-flash", p.GetModel())
	require.Equal(t, ai.BackendGemini, p.GetBackend())

	p2 := NewGemini(ai.Config{Model: "gemini-2.5-pro", APIKey: "key"})
	require.Equal(t, "gemini-2.5-pro", p2.GetModel())
}

func TestGeminiNoAPIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	p := NewGemini(ai.Config{Model: "gemini-2.0-flash"})
	_, err := p.Execute(context.Background(), ai.Request{Prompt: "hello"})
	require.Error(t, err)
}

func TestGeminiIntegration(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	p := NewGemini(ai.Config{APIKey: apiKey})
	resp, err := p.Execute(context.Background(), ai.Request{
		Prompt:    "Reply with exactly: hello",
		MaxTokens: 32,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Text)
	require.Equal(t, ai.BackendGemini, resp.Backend)
	require.Greater(t, resp.Usage.InputTokens, 0)
	require.Greater(t, resp.Usage.OutputTokens, 0)
}

func TestGeminiStructuredIntegration(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	type Capital struct {
		City    string `json:"city"`
		Country string `json:"country"`
	}

	p := NewGemini(ai.Config{APIKey: apiKey})
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
