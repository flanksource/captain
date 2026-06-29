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

	assert.Contains(t, req.Prompt.System, "Conventional Commit")
	assert.Contains(t, req.Prompt.User, "+added a line")
	assert.NotContains(t, req.Prompt.User, "Conventional Commit", "system text must not leak into the user prompt")

	assert.Equal(t, "claude-sonnet-4-6", cfg.Model.Name)
	assert.Equal(t, ai.BackendAnthropic, cfg.Model.Backend, "model name should infer the anthropic backend")
	assert.Equal(t, 1024, req.Budget.MaxTokens)
	temp, ok := req.Temp()
	require.True(t, ok, "temperature should be set from frontmatter")
	assert.InDelta(t, 0.2, temp, 1e-9)
}

func TestRender_StructuredOutputTarget(t *testing.T) {
	type commitMsg struct {
		Type    string `json:"type"`
		Subject string `json:"subject"`
	}
	out := &commitMsg{}

	req, _, err := Load("{{role \"user\"}}\nhi").Render(nil, out)
	require.NoError(t, err)
	assert.Same(t, out, req.Prompt.Schema)
}

func TestLibrary_Render(t *testing.T) {
	lib := NewLibrary(library)
	req, cfg, err := lib.Render("testdata/commit.prompt", map[string]any{"diff": "x"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", cfg.Model.Name)
	assert.Contains(t, req.Prompt.User, "x")
}
