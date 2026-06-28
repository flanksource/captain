package prompt

import (
	"embed"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/commit.prompt
var library embed.FS

func TestRender_FrontmatterAndMessages(t *testing.T) {
	tmpl, err := LoadFS(library, "testdata/commit.prompt")
	require.NoError(t, err)

	req, cfg, err := tmpl.Render(map[string]any{"diff": "+added a line"}, nil)
	require.NoError(t, err)

	assert.Contains(t, req.SystemPrompt, "Conventional Commit")
	assert.Contains(t, req.Prompt, "+added a line")
	assert.NotContains(t, req.Prompt, "Conventional Commit", "system text must not leak into the user prompt")

	assert.Equal(t, "claude-sonnet-4-6", cfg.Model)
	assert.Equal(t, ai.BackendAnthropic, cfg.Backend, "model name should infer the anthropic backend")
	assert.Equal(t, 1024, req.MaxTokens)
	assert.InDelta(t, 0.2, req.Temperature, 1e-9)
}

func TestRender_StructuredOutputTarget(t *testing.T) {
	type commitMsg struct {
		Type    string `json:"type"`
		Subject string `json:"subject"`
	}
	out := &commitMsg{}

	req, _, err := Load("{{role \"user\"}}\nhi").Render(nil, out)
	require.NoError(t, err)
	assert.Same(t, out, req.StructuredOutput)
}

func TestLibrary_Render(t *testing.T) {
	lib := NewLibrary(library)
	req, cfg, err := lib.Render("testdata/commit.prompt", map[string]any{"diff": "x"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", cfg.Model)
	assert.Contains(t, req.Prompt, "x")
}
