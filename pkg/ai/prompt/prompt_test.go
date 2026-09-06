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

	req, cfg, err := tmpl.Render(RenderOptions{Data: map[string]any{
		"patch":        "+func Login() bool { return a < b && c > d }",
		"maxBodyLines": 3,
	}})
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

	withoutCap, _, err := tmpl.Render(RenderOptions{Data: map[string]any{
		"patch":        "+trivial change",
		"maxBodyLines": 0,
	}})
	require.NoError(t, err)
	assert.Contains(t, withoutCap.Prompt.User, "body: omit unless the change is non-trivial")
	assert.NotContains(t, withoutCap.Prompt.User, "body: at most")

	assert.Equal(t, "claude-sonnet-4-6", cfg.Model.Name)
	assert.Equal(t, ai.Anthropic, cfg.Model.Provider, "the model name names the anthropic family")
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

	req, _, err := tmpl.Render(RenderOptions{Data: map[string]any{"target": "parser.go"}})
	require.NoError(t, err)

	// Spec-native keys from the second parse.
	require.NotNil(t, req.Sandbox)
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

	req, _, err := Load("{{role \"user\"}}\nhi").Render(RenderOptions{Output: out})
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

	req, _, err := Load(src).Render(RenderOptions{})
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
	req, cfg, err := lib.Render("testdata/commit.prompt", RenderOptions{Data: map[string]any{
		"patch":        "x",
		"maxBodyLines": 0,
	}})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", cfg.Model.Name)
	assert.Contains(t, req.Prompt.User, "x")
}

// TestRender_AuthoredModeSelectsRuntime pins the contract that `mode:` names the
// mechanism and, with the model name's family, is the whole runtime. The name
// used to smuggle a mode of its own, so `opus` + `agent` could resolve to the
// Anthropic API — a wrong-runtime execution rather than a loud failure.
func TestRender_AuthoredModeSelectsRuntime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		model   string
		runtime api.Runtime
	}{
		{name: "agent mode", mode: "agent", model: "opus", runtime: api.RuntimeOf(api.Anthropic, api.ModeAgent)},
		{name: "cli mode", mode: "cli", model: "opus", runtime: api.RuntimeOf(api.Anthropic, api.ModeCLI)},
		{name: "cmux mode", mode: "cmux", model: "opus", runtime: api.RuntimeOf(api.Anthropic, api.ModeCmux)},
		{name: "api mode", mode: "api", model: "opus", runtime: api.RuntimeOf(api.Anthropic, api.ModeAPI)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, cfg, err := Load("---\nmodel: " + tc.model + "\nmode: " + tc.mode + "\n---\n{{role \"user\"}}\nGo.\n").
				Render(RenderOptions{})
			require.NoError(t, err)
			assert.Equal(t, tc.runtime, api.RuntimeOf(cfg.Model.Provider, cfg.Model.Mode), "the config runtime must follow the authored mode")
			assert.Equal(t, tc.runtime, api.RuntimeOf(req.Model.Provider, req.Model.Mode), "the request runtime must follow the authored mode")
			assert.Equal(t, api.RuntimeMode(tc.mode), cfg.Model.Mode, "authored mode must survive resolution")
		})
	}
}

func TestRender_RuntimeFixtureExamples(t *testing.T) {
	expected := map[string]struct {
		runtime api.Runtime
		model   string
	}{
		"testdata/fixtures/anthropic-claude-opus.prompt":   {runtime: api.RuntimeOf(api.Anthropic, api.ModeAPI), model: "claude-opus-4-6"},
		"testdata/fixtures/anthropic-claude-sonnet.prompt": {runtime: api.RuntimeOf(api.Anthropic, api.ModeAPI), model: "claude-sonnet-4-6"},
		"testdata/fixtures/claude-agent-opus.prompt":       {runtime: api.RuntimeOf(api.Anthropic, api.ModeAgent), model: "claude-opus-5"},
		"testdata/fixtures/claude-agent-sonnet.prompt":     {runtime: api.RuntimeOf(api.Anthropic, api.ModeAgent), model: "claude-sonnet-5"},
		"testdata/fixtures/claude-cmux-opus.prompt":        {runtime: api.RuntimeOf(api.Anthropic, api.ModeCmux), model: "claude-opus-5"},
		"testdata/fixtures/claude-cmux-sonnet.prompt":      {runtime: api.RuntimeOf(api.Anthropic, api.ModeCmux), model: "claude-sonnet-5"},
		"testdata/fixtures/claude-cli-opus.prompt":         {runtime: api.RuntimeOf(api.Anthropic, api.ModeCLI), model: "claude-opus-5"},
		"testdata/fixtures/claude-cli-sonnet.prompt":       {runtime: api.RuntimeOf(api.Anthropic, api.ModeCLI), model: "claude-sonnet-5"},
		"testdata/fixtures/codex-agent.prompt":             {runtime: api.RuntimeOf(api.OpenAI, api.ModeAgent), model: "gpt-5-codex"},
		"testdata/fixtures/codex-cmux.prompt":              {runtime: api.RuntimeOf(api.OpenAI, api.ModeCmux), model: "gpt-5-codex"},
		"testdata/fixtures/deepseek.prompt":                {runtime: api.RuntimeOf(api.DeepSeek, api.ModeAPI), model: "deepseek-reasoner"},
		"testdata/fixtures/codex-cli.prompt":               {runtime: api.RuntimeOf(api.OpenAI, api.ModeCLI), model: "gpt-5-codex"},
		"testdata/fixtures/gemini-api.prompt":              {runtime: api.RuntimeOf(api.Google, api.ModeAPI), model: "gemini-2.5-pro"},
		"testdata/fixtures/gemini-cli.prompt":              {runtime: api.RuntimeOf(api.Google, api.ModeCLI), model: "gemini-2.5-pro"},
		"testdata/fixtures/openai-gpt.prompt":              {runtime: api.RuntimeOf(api.OpenAI, api.ModeAPI), model: "gpt-5"},
	}

	seenFiles := map[string]bool{}
	seenRuntimes := map[api.Runtime]bool{}
	claudeFamilies := map[api.Runtime]map[string]bool{
		api.RuntimeOf(api.Anthropic, api.ModeAPI):   {},
		api.RuntimeOf(api.Anthropic, api.ModeAgent): {},
		api.RuntimeOf(api.Anthropic, api.ModeCLI):   {},
		api.RuntimeOf(api.Anthropic, api.ModeCmux):  {},
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
		req, cfg, err := tmpl.Render(RenderOptions{Data: map[string]any{"task": "summarize the change and propose next steps"}})
		require.NoError(t, err)
		require.NoError(t, req.Validate())

		assert.Equal(t, path, req.Prompt.Source)
		assert.Contains(t, req.Prompt.User, "summarize the change")
		assert.Equal(t, want.model, req.Model.Name)
		assert.Equal(t, want.model, cfg.Model.Name)
		assert.Equal(t, want.runtime, api.RuntimeOf(req.Model.Provider, req.Model.Mode))
		assert.Equal(t, want.runtime, api.RuntimeOf(cfg.Model.Provider, cfg.Model.Mode))

		seenRuntimes[want.runtime] = true
		if families, ok := claudeFamilies[want.runtime]; ok {
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
	for _, runtime := range api.AllRuntimes() {
		assert.True(t, seenRuntimes[runtime], "missing fixture for runtime %v", runtime)
	}
	for runtime, families := range claudeFamilies {
		assert.True(t, families["sonnet"], "missing sonnet fixture for runtime %v", runtime)
		assert.True(t, families["opus"], "missing opus fixture for runtime %v", runtime)
	}
}
