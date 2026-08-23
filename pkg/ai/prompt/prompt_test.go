package prompt

import (
	"embed"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata
var library embed.FS

func TestRender_FrontmatterAndMessages(t *testing.T) {
	tmpl, err := LoadFS(library, "testdata/commit.prompt")
	require.NoError(t, err)

	req, cfg, err := tmpl.Render(map[string]any{
		"patch":        "+func Login() bool { return a < b && c > d }",
		"maxBodyLines": 3,
	}, nil)
	require.NoError(t, err)

	assert.Contains(t, req.Prompt.System, "commit message generator")
	assert.Contains(t, req.Prompt.User, "a < b && c > d")
	assert.NotContains(t, req.Prompt.User, "&lt;", "patch content must not be HTML-escaped")
	assert.Contains(t, req.Prompt.User, "body: at most 3 line(s)")
	assert.NotContains(t, req.Prompt.User, "commit message generator", "system text must not leak into the user prompt")
	assert.JSONEq(t, `{
		"type": "object",
		"additionalProperties": false,
		"required": ["type", "subject"],
		"properties": {
			"type": {
				"type": "string",
				"description": "Conventional commit type: feat|fix|perf|refactor|test|docs|build|ci|chore|revert"
			},
			"scope": {
				"type": "string",
				"description": "Optional scope, e.g. db, api, fe, kubernetes"
			},
			"subject": {
				"type": "string",
				"description": "Imperative subject line, max 100 chars, no trailing period"
			},
			"body": {
				"type": "string",
				"description": "Optional body explaining why and impact"
			}
		}
	}`, string(req.Prompt.SchemaJSON))

	withoutCap, _, err := tmpl.Render(map[string]any{
		"patch":        "+trivial change",
		"maxBodyLines": 0,
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, withoutCap.Prompt.User, "body: omit unless the change is non-trivial")
	assert.NotContains(t, withoutCap.Prompt.User, "body: at most")

	assert.Equal(t, "claude-sonnet-4-6", cfg.Model.Name)
	assert.Equal(t, ai.BackendAnthropic, cfg.Model.Backend, "model name should infer the anthropic backend")
	assert.Equal(t, 1024, req.Budget.MaxTokens)
	temp, ok := req.Temp()
	require.True(t, ok, "temperature should be set from frontmatter")
	assert.InDelta(t, 0.2, temp, 1e-9)
}

// TestRender_SpecFrontmatter exercises the second parse: spec-native frontmatter
// (permissions/memory/budget) lands on the nested ai.Request groups,
// while the dotprompt config: block stays canonical for the knobs it owns.
func TestRender_SpecFrontmatter(t *testing.T) {
	tmpl, err := LoadFS(library, "testdata/options.prompt")
	require.NoError(t, err)

	req, _, err := tmpl.Render(map[string]any{"target": "parser.go"}, nil)
	require.NoError(t, err)

	// Spec-native keys from the second parse.
	assert.Equal(t, api.PermissionAcceptEdits, req.Permissions.Mode)
	assert.Equal(t, []api.Preset{api.PresetEdit}, req.Permissions.Presets)
	assert.Equal(t, []string{"Edit", "Read"}, req.Permissions.Tools.AllowList())
	assert.True(t, req.Permissions.MCP.Disabled)
	assert.True(t, req.Memory.SkipUser)
	assert.Equal(t, 3, req.Budget.MaxTurns)

	// An ordered toolPolicy: block reaches the spec verbatim, which is what lets
	// a .prompt govern tools on a non-chat agent run. Order is the contract, so
	// it is asserted as a sequence rather than a set.
	require.Len(t, req.ToolPolicy, 2)
	assert.Equal(t, api.MatchPatterns{"provider.*"}, req.ToolPolicy[0].Group)
	assert.Equal(t, api.ToolPolicyDeny, req.ToolPolicy[0].Policy)
	assert.Equal(t, api.MatchPatterns{"Read"}, req.ToolPolicy[1].Name)
	assert.Equal(t, api.ToolPolicyAllow, req.ToolPolicy[1].Policy)
	require.NotNil(t, req.ToolPolicy[1].ReadOnly)
	assert.True(t, *req.ToolPolicy[1].ReadOnly)

	// The dotprompt config: block wins for the knobs it owns: config.maxOutputTokens
	// (1024) overrides the spec-native budget.maxTokens (5000), and config.temperature
	// sets the model temperature.
	assert.Equal(t, 1024, req.Budget.MaxTokens)
	temp, ok := req.Temp()
	require.True(t, ok)
	assert.InDelta(t, 0.2, temp, 1e-9)

	// Body still wins for the messages.
	assert.Contains(t, req.Prompt.System, "refactoring assistant")
	assert.Contains(t, req.Prompt.User, "parser.go")
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
	assert.Empty(t, req.Prompt.SchemaJSON, "a Go target takes precedence over any frontmatter schema")
}

// A frontmatter output.schema is resolved and marshalled onto SchemaJSON when no
// Go target is passed, so schemas can be declared in the .prompt file.
func TestRender_FrontmatterOutputSchema(t *testing.T) {
	src := "---\n" +
		"output:\n" +
		"  schema:\n" +
		"    type: object\n" +
		"    additionalProperties: false\n" +
		"    properties:\n" +
		"      title:\n" +
		"        type: string\n" +
		"    required:\n" +
		"      - title\n" +
		"---\n" +
		"{{role \"user\"}}\nname a PR"

	req, _, err := Load(src).Render(nil, nil)
	require.NoError(t, err)
	require.Nil(t, req.Prompt.Schema, "no Go target was passed")
	require.NotEmpty(t, req.Prompt.SchemaJSON, "frontmatter output.schema must reach SchemaJSON")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(req.Prompt.SchemaJSON, &decoded))
	assert.Equal(t, "object", decoded["type"])
	props, ok := decoded["properties"].(map[string]any)
	require.True(t, ok, "resolved schema must carry properties")
	assert.Contains(t, props, "title")
}

func TestLibrary_Render(t *testing.T) {
	lib := NewLibrary(library)
	req, cfg, err := lib.Render("testdata/commit.prompt", map[string]any{
		"patch":        "x",
		"maxBodyLines": 0,
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", cfg.Model.Name)
	assert.Contains(t, req.Prompt.User, "x")
}

func TestRender_BackendFixtureExamples(t *testing.T) {
	expected := map[string]struct {
		backend api.Backend
		model   string
	}{
		"testdata/fixtures/anthropic-claude-opus.prompt":   {backend: api.BackendAnthropic, model: "claude-opus-4-6"},
		"testdata/fixtures/anthropic-claude-sonnet.prompt": {backend: api.BackendAnthropic, model: "claude-sonnet-4-6"},
		"testdata/fixtures/claude-agent-opus.prompt":       {backend: api.BackendClaudeAgent, model: "claude-agent-opus"},
		"testdata/fixtures/claude-agent-sonnet.prompt":     {backend: api.BackendClaudeAgent, model: "claude-agent-sonnet"},
		"testdata/fixtures/claude-cmux-opus.prompt":        {backend: api.BackendClaudeCmux, model: "claude-cmux-opus"},
		"testdata/fixtures/claude-cmux-sonnet.prompt":      {backend: api.BackendClaudeCmux, model: "claude-cmux-sonnet"},
		"testdata/fixtures/claude-cli-opus.prompt":         {backend: api.BackendClaudeCLI, model: "claude-agent-opus"},
		"testdata/fixtures/claude-cli-sonnet.prompt":       {backend: api.BackendClaudeCLI, model: "claude-agent-sonnet"},
		"testdata/fixtures/codex-agent.prompt":             {backend: api.BackendCodexAgent, model: "gpt-5-codex"},
		"testdata/fixtures/codex-cmux.prompt":              {backend: api.BackendCodexCmux, model: "gpt-5-codex"},
		"testdata/fixtures/deepseek.prompt":                {backend: api.BackendDeepSeek, model: "deepseek-reasoner"},
		"testdata/fixtures/codex-cli.prompt":               {backend: api.BackendCodexCLI, model: "gpt-5-codex"},
		"testdata/fixtures/gemini-api.prompt":              {backend: api.BackendGemini, model: "gemini-2.5-pro"},
		"testdata/fixtures/gemini-cli.prompt":              {backend: api.BackendGeminiCLI, model: "gemini-cli-pro"},
		"testdata/fixtures/openai-gpt.prompt":              {backend: api.BackendOpenAI, model: "gpt-5"},
	}

	seenFiles := map[string]bool{}
	seenBackends := map[api.Backend]bool{}
	claudeFamilies := map[api.Backend]map[string]bool{
		api.BackendAnthropic:   {},
		api.BackendClaudeAgent: {},
		api.BackendClaudeCLI:   {},
		api.BackendClaudeCmux:  {},
	}

	err := fs.WalkDir(library, "testdata/fixtures", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".prompt" {
			return nil
		}

		want, ok := expected[path]
		require.True(t, ok, "unexpected fixture %s", path)
		seenFiles[path] = true

		tmpl, err := LoadFS(library, path)
		require.NoError(t, err)
		req, cfg, err := tmpl.Render(map[string]any{"task": "summarize the change and propose next steps"}, nil)
		require.NoError(t, err)
		require.NoError(t, req.Validate())

		assert.Equal(t, path, req.Prompt.Source)
		assert.Contains(t, req.Prompt.User, "summarize the change")
		assert.Equal(t, want.model, req.Model.Name)
		assert.Equal(t, want.model, cfg.Model.Name)
		assert.Equal(t, want.backend, req.Model.Backend)
		assert.Equal(t, want.backend, cfg.Model.Backend)

		seenBackends[want.backend] = true
		if families, ok := claudeFamilies[want.backend]; ok {
			switch model := strings.ToLower(want.model); {
			case strings.Contains(model, "sonnet"):
				families["sonnet"] = true
			case strings.Contains(model, "opus"):
				families["opus"] = true
			}
		}
		return nil
	})
	require.NoError(t, err)

	assert.Len(t, seenFiles, len(expected))
	for path := range expected {
		assert.True(t, seenFiles[path], "missing fixture %s", path)
	}
	for _, backend := range api.AllBackends() {
		assert.True(t, seenBackends[backend], "missing fixture for backend %s", backend)
	}
	for backend, families := range claudeFamilies {
		assert.True(t, families["sonnet"], "missing sonnet fixture for backend %s", backend)
		assert.True(t, families["opus"], "missing opus fixture for backend %s", backend)
	}
}
