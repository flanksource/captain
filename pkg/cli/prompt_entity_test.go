package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/commons-db/shell"
)

func TestPromptEntityListsEmbeddedExamples(t *testing.T) {
	isolateCaptainConfig(t)

	prompts, err := listPrompts(context.Background(), PromptListOptions{Source: "embedded"})
	if err != nil {
		t.Fatalf("listPrompts() err = %v", err)
	}

	var commit *PromptSummary
	for i := range prompts {
		if prompts[i].RelPath == "testdata/commit.prompt" {
			commit = &prompts[i]
			break
		}
	}
	if commit == nil {
		t.Fatalf("embedded commit.prompt not found in %d prompts", len(prompts))
	}
	if commit.Writable {
		t.Fatalf("embedded prompt reported writable")
	}
	if commit.Model != "claude-sonnet-4-6" {
		t.Fatalf("embedded prompt model = %q, want claude-sonnet-4-6", commit.Model)
	}
	if len(commit.Variables) != 1 || commit.Variables[0].Name != "diff" {
		t.Fatalf("embedded prompt variables = %+v, want diff", commit.Variables)
	}
}

func TestPromptEntityUsesProjectPromptDirFallback(t *testing.T) {
	isolateCaptainConfig(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	defaultDir := filepath.Join(cwd, ".captain", "prompts")

	local, err := listPrompts(context.Background(), PromptListOptions{Source: "local"})
	if err != nil {
		t.Fatalf("listPrompts(local) err = %v", err)
	}
	if len(local) != 0 {
		t.Fatalf("local prompt count = %d, want 0", len(local))
	}
	if _, err := os.Stat(defaultDir); !os.IsNotExist(err) {
		t.Fatalf("default prompt dir stat err = %v, want not exist", err)
	}

	created, err := createPrompt(context.Background(), map[string]any{
		"name": "Fallback",
		"content": `---
name: Fallback
---
{{role "user"}}
Hello from fallback
`,
	})
	if err != nil {
		t.Fatalf("createPrompt() err = %v", err)
	}
	if !created.Writable {
		t.Fatalf("created prompt reported read-only")
	}
	wantPath := filepath.Join(defaultDir, "fallback.prompt")
	if created.Path != wantPath {
		t.Fatalf("created path = %q, want %q", created.Path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("created prompt missing at fallback path: %v", err)
	}
}

func TestPromptEntityCreatesUpdatesRendersAndDeletesLocalPrompt(t *testing.T) {
	isolateCaptainConfig(t)

	dir := t.TempDir()
	ctx := ContextWithPromptDirs(context.Background(), []string{dir})
	content := `---
name: Greeting
description: Test prompt
model: claude-sonnet-4-6
input:
  schema:
    name: string
---
{{role "user"}}
Hello {{name}}
`

	created, err := createPrompt(ctx, map[string]any{
		"name":    "Greeting",
		"content": content,
	})
	if err != nil {
		t.Fatalf("createPrompt() err = %v", err)
	}
	if !created.Writable {
		t.Fatalf("created prompt reported read-only")
	}
	if created.RelPath != "greeting.prompt" {
		t.Fatalf("created relPath = %q, want greeting.prompt", created.RelPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "greeting.prompt")); err != nil {
		t.Fatalf("created prompt file missing: %v", err)
	}

	localPrompts, err := listPrompts(ctx, PromptListOptions{Source: "local", Query: "greet"})
	if err != nil {
		t.Fatalf("list local prompts: %v", err)
	}
	if len(localPrompts) != 1 {
		t.Fatalf("local prompt count = %d, want 1", len(localPrompts))
	}
	if err := captainconfig.Save(captainconfig.Config{AI: captainconfig.AIDefaults{
		Backend: "codex-cli",
		Model:   "gpt-5-codex",
	}}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	rendered, err := renderPrompt(ctx, created.ID, PromptRenderRequest{
		Variables: map[string]any{"name": "Ada"},
	})
	if err != nil {
		t.Fatalf("renderPrompt() err = %v", err)
	}
	if rendered.ValidationError != "" {
		t.Fatalf("render validation error = %q", rendered.ValidationError)
	}
	if !strings.Contains(rendered.User, "Hello Ada") {
		t.Fatalf("rendered user prompt = %q, want greeting", rendered.User)
	}
	if rendered.Model != "claude-sonnet-4-6" || rendered.Backend != "anthropic" {
		t.Fatalf("rendered model/backend = %s/%s, want claude-sonnet-4-6/anthropic", rendered.Model, rendered.Backend)
	}
	if rendered.Input.Cwd() == "" || !filepath.IsAbs(rendered.Input.Cwd()) {
		t.Fatalf("rendered setup cwd = %q, want absolute", rendered.Input.Cwd())
	}

	updated, err := updatePrompt(ctx, created.ID, map[string]any{
		"content": strings.Replace(content, "Hello {{name}}", "Goodbye {{name}}", 1),
	})
	if err != nil {
		t.Fatalf("updatePrompt() err = %v", err)
	}
	if !strings.Contains(updated.Content, "Goodbye") {
		t.Fatalf("updated content did not persist: %q", updated.Content)
	}

	if err := deletePrompt(ctx, created.ID); err != nil {
		t.Fatalf("deletePrompt() err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "greeting.prompt")); !os.IsNotExist(err) {
		t.Fatalf("deleted prompt stat err = %v, want not exist", err)
	}
}

func TestPromptEntityExposesOutputSchema(t *testing.T) {
	isolateCaptainConfig(t)

	dir := t.TempDir()
	ctx := ContextWithPromptDirs(context.Background(), []string{dir})
	content := `---
name: Structured
model: claude-sonnet-4-6
input:
  schema:
    topic: string
output:
  schema:
    answer: string
    score: integer
---
{{role "user"}}
Summarize {{topic}}
`

	created, err := createPrompt(ctx, map[string]any{"name": "Structured", "content": content})
	if err != nil {
		t.Fatalf("createPrompt() err = %v", err)
	}

	// get → PromptDetail carries the frontmatter output.schema.
	detail, err := getPrompt(ctx, created.ID)
	if err != nil {
		t.Fatalf("getPrompt() err = %v", err)
	}
	assertSchemaHasProps(t, "detail.OutputSchema", detail.OutputSchema, "answer", "score")

	// render → PromptRenderResult carries the same output schema.
	rendered, err := renderPrompt(ctx, created.ID, PromptRenderRequest{
		Variables: map[string]any{"topic": "Go"},
	})
	if err != nil {
		t.Fatalf("renderPrompt() err = %v", err)
	}
	if rendered.ValidationError != "" {
		t.Fatalf("render validation error = %q", rendered.ValidationError)
	}
	assertSchemaHasProps(t, "rendered.OutputSchema", rendered.OutputSchema, "answer", "score")
}

// assertSchemaHasProps checks a JSON-Schema map exposes the given top-level
// properties, tolerating the exact picoschema-expanded shape.
func assertSchemaHasProps(t *testing.T, label string, schema map[string]any, keys ...string) {
	t.Helper()
	if schema == nil {
		t.Fatalf("%s is nil, want an object schema", label)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s.properties = %T, want map[string]any", label, schema["properties"])
	}
	for _, k := range keys {
		if _, ok := props[k]; !ok {
			t.Fatalf("%s.properties missing %q; got %v", label, k, props)
		}
	}
}

func TestUpdateEmbeddedPromptForksToLocal(t *testing.T) {
	isolateCaptainConfig(t)

	dir := t.TempDir()
	ctx := ContextWithPromptDirs(context.Background(), []string{dir})

	embedded, err := findEmbeddedPrompt(ctx, "testdata/commit.prompt")
	if err != nil {
		t.Fatal(err)
	}
	if embedded.Writable {
		t.Fatalf("embedded prompt reported writable")
	}

	original, err := getPrompt(ctx, embedded.ID)
	if err != nil {
		t.Fatalf("getPrompt(embedded) err = %v", err)
	}
	newContent := original.Content + "\n{{! local override }}\n"

	forked, err := updatePrompt(ctx, embedded.ID, map[string]any{"content": newContent})
	if err != nil {
		t.Fatalf("updatePrompt(embedded) err = %v", err)
	}
	if !forked.Writable || forked.SourceKind != "local" {
		t.Fatalf("forked prompt = kind %q writable %v, want local writable", forked.SourceKind, forked.Writable)
	}
	if forked.ID == embedded.ID {
		t.Fatalf("forked prompt kept embedded id %q", forked.ID)
	}
	if forked.RelPath != "commit.prompt" {
		t.Fatalf("forked relPath = %q, want commit.prompt (testdata/ stripped)", forked.RelPath)
	}
	if !strings.Contains(forked.Content, "local override") {
		t.Fatalf("forked content did not persist edit: %q", forked.Content)
	}
	if _, err := os.Stat(filepath.Join(dir, "commit.prompt")); err != nil {
		t.Fatalf("forked prompt file missing: %v", err)
	}

	stillEmbedded, err := getPrompt(ctx, embedded.ID)
	if err != nil {
		t.Fatalf("getPrompt(embedded) after fork err = %v", err)
	}
	if strings.Contains(stillEmbedded.Content, "local override") {
		t.Fatalf("embedded prompt was mutated by fork")
	}

	if _, err := updatePrompt(ctx, embedded.ID, map[string]any{"content": newContent}); err == nil {
		t.Fatalf("second fork of same prompt should fail with already-exists")
	}
}

func findEmbeddedPrompt(ctx context.Context, relPath string) (PromptSummary, error) {
	prompts, err := listPrompts(ctx, PromptListOptions{Source: "embedded"})
	if err != nil {
		return PromptSummary{}, err
	}
	for _, prompt := range prompts {
		if prompt.RelPath == relPath {
			return prompt, nil
		}
	}
	return PromptSummary{}, fmt.Errorf("embedded prompt %q not found", relPath)
}

func TestRenderPromptAppliesRuntimeSpec(t *testing.T) {
	isolateCaptainConfig(t)

	dir := t.TempDir()
	cwd := t.TempDir()
	t.Chdir(cwd)
	ctx := ContextWithPromptDirs(context.Background(), []string{dir})
	content := `---
name: Runtime Spec
model: claude-sonnet-4-6
---
{{role "user"}}
Hello {{name}}
`
	created, err := createPrompt(ctx, map[string]any{
		"name":    "Runtime Spec",
		"content": content,
	})
	if err != nil {
		t.Fatalf("createPrompt() err = %v", err)
	}

	temp := 0.2
	rendered, err := renderPrompt(ctx, created.ID, PromptRenderRequest{
		Variables: map[string]any{"name": "Ada"},
		Spec: &api.Spec{
			Model: api.Model{
				Name:        "gpt-4o",
				ID:          "openai/gpt-4o",
				Backend:     api.BackendOpenAI,
				Temperature: &temp,
				Effort:      api.EffortLow,
				NoCache:     true,
			},
			Prompt: api.Prompt{
				System:       "runtime system",
				AppendSystem: "runtime append",
				Source:       "runtime-source",
				Metadata:     map[string]string{"surface": "prompt-ui"},
			},
			Budget: api.Budget{Cost: 0.5, MaxTokens: 1234, MaxTurns: 4, Timeout: "90s"},
			Permissions: api.Permissions{
				Mode:    api.PermissionAcceptEdits,
				Presets: []api.Preset{api.PresetEdit},
				Tools: api.Tools{
					Allow: []string{"Read"},
					Deny:  []string{"Bash"},
					Modes: map[string]api.ToolMode{"Bash": api.ToolModeDisabled},
				},
				MCP: api.MCP{
					Disabled: true,
					Servers:  []string{"filesystem"},
					Modes:    api.ResourcePolicies{"gavel": api.ResourceDisabled},
				},
				Plugins: api.ResourcePolicies{"/plugins": api.ResourceEnabled},
				Skills:  api.ResourcePolicies{"/permission-skills": api.ResourceEnabled},
			},
			Memory: api.Memory{
				Skills:     []string{"/skills"},
				SkipUser:   true,
				SkipMemory: true,
				Bare:       true,
			},
			Setup: &shell.Setup{
				Cwd:    "workspace",
				DotEnv: []string{".env"},
				Checkout: &shell.Checkout{
					Mode: shell.CheckoutLocal,
					Path: "/repo",
					Ref:  "abc123",
					Worktree: &shell.Worktree{
						Mode:   shell.WorktreeNew,
						Prefix: "runtime-branch",
						Keep:   true,
					},
				},
			},
			SessionID: "sess-runtime",
		},
	})
	if err != nil {
		t.Fatalf("renderPrompt() err = %v", err)
	}
	if rendered.ValidationError != "" {
		t.Fatalf("render validation error = %q", rendered.ValidationError)
	}
	if rendered.Model != "gpt-4o" || rendered.Backend != "openai" {
		t.Fatalf("rendered model/backend = %s/%s, want gpt-4o/openai", rendered.Model, rendered.Backend)
	}
	if rendered.Config.Model.ID != "openai/gpt-4o" {
		t.Fatalf("config model ID = %q, want openai/gpt-4o", rendered.Config.Model.ID)
	}
	if rendered.Input.Temperature == nil || *rendered.Input.Temperature != temp {
		t.Fatalf("temperature = %v, want %v", rendered.Input.Temperature, temp)
	}
	if rendered.Input.Budget.Cost != 0.5 || rendered.Input.Budget.MaxTokens != 1234 ||
		rendered.Input.Budget.MaxTurns != 4 || rendered.Input.Budget.Timeout != "90s" {
		t.Fatalf("budget = %+v, want cost/maxTokens override", rendered.Input.Budget)
	}
	if !rendered.Input.Model.NoCache || !rendered.Config.NoCache {
		t.Fatalf("noCache = input %v config %v, want true", rendered.Input.Model.NoCache, rendered.Config.NoCache)
	}
	if rendered.Input.Prompt.System != "runtime system" || rendered.Input.Prompt.AppendSystem != "runtime append" {
		t.Fatalf("prompt system fields = %+v, want runtime overrides", rendered.Input.Prompt)
	}
	if rendered.Input.Prompt.Source != "runtime-source" || rendered.Input.Prompt.Metadata["surface"] != "prompt-ui" {
		t.Fatalf("prompt source/metadata = %+v, want runtime overrides", rendered.Input.Prompt)
	}
	if rendered.Input.Permissions.Mode != api.PermissionAcceptEdits ||
		rendered.Input.Permissions.Tools.Modes["Bash"] != api.ToolModeDisabled ||
		!rendered.Input.Permissions.MCP.Disabled {
		t.Fatalf("permissions = %+v, want runtime overrides", rendered.Input.Permissions)
	}
	if rendered.Input.Permissions.Plugins["/plugins"] != api.ResourceEnabled {
		t.Fatalf("plugins = %+v, want enabled runtime plugin", rendered.Input.Permissions.Plugins)
	}
	if !strings.Contains(strings.Join(rendered.Input.Memory.Skills, ","), "/permission-skills") {
		t.Fatalf("skills = %+v, want permission skills merged into memory skills", rendered.Input.Memory.Skills)
	}
	if !rendered.Input.Memory.SkipUser || !rendered.Input.Memory.SkipMemory || !rendered.Input.Memory.Bare {
		t.Fatalf("memory = %+v, want runtime overrides", rendered.Input.Memory)
	}
	if rendered.Input.Cwd() != filepath.Join(cwd, "workspace") {
		t.Fatalf("setup cwd = %q, want cwd-relative runtime dir", rendered.Input.Cwd())
	}
	if rendered.Input.Setup == nil || rendered.Input.Setup.Checkout == nil || rendered.Input.Setup.Checkout.Ref != "abc123" {
		t.Fatalf("setup checkout = %+v, want runtime git checkout overlay", rendered.Input.Setup)
	}
	if rendered.Input.Setup.Checkout.Worktree == nil || !rendered.Input.Setup.Checkout.Worktree.Keep {
		t.Fatalf("worktree setup = %+v, want runtime worktree overlay", rendered.Input.Setup.Checkout.Worktree)
	}
	if rendered.Input.SessionID != "sess-runtime" {
		t.Fatalf("runtime session = input=%+v", rendered.Input)
	}
}

func TestRenderPromptEphemeralSpec(t *testing.T) {
	isolateCaptainConfig(t)

	cwd := t.TempDir()
	t.Chdir(cwd)
	temp := 0.1
	rendered, err := renderPrompt(context.Background(), "", PromptRenderRequest{
		Spec: &api.Spec{
			Model: api.Model{
				Name:        "gpt-5.5",
				Backend:     api.BackendCodexAgent,
				Temperature: &temp,
				Effort:      api.EffortHigh,
			},
			Prompt: api.Prompt{
				System: "scratch system",
				User:   "Draft a deployment plan",
			},
			Budget: api.Budget{Timeout: "2h"},
		},
	})
	if err != nil {
		t.Fatalf("renderPrompt() err = %v", err)
	}
	if rendered.ValidationError != "" {
		t.Fatalf("render validation error = %q", rendered.ValidationError)
	}
	if rendered.ID != "" || rendered.Name != "Scratch Prompt" {
		t.Fatalf("rendered prompt identity = id %q name %q, want scratch prompt", rendered.ID, rendered.Name)
	}
	if rendered.User != "Draft a deployment plan" || rendered.System != "scratch system" {
		t.Fatalf("rendered prompt = user %q system %q", rendered.User, rendered.System)
	}
	if rendered.Model != "gpt-5.5" || rendered.Backend != "codex-agent" {
		t.Fatalf("rendered model/backend = %s/%s, want gpt-5.5/codex-agent", rendered.Model, rendered.Backend)
	}
	if rendered.Input.Prompt.Source != "<ephemeral>" {
		t.Fatalf("prompt source = %q, want <ephemeral>", rendered.Input.Prompt.Source)
	}
	if rendered.Input.Cwd() != cwd {
		t.Fatalf("setup cwd = %q, want %q", rendered.Input.Cwd(), cwd)
	}
}

func TestApplyPromptDefaultsSelectorEffortWins(t *testing.T) {
	isolateCaptainConfig(t)
	req := ai.Request{Model: api.Model{Effort: api.EffortLow}}
	cfg := ai.Config{Model: api.Model{
		Name:    "gpt-5.6-sol",
		Backend: api.BackendCodexAgent,
		Effort:  api.EffortHigh,
	}}
	if err := applyPromptDefaults(&req, &cfg); err != nil {
		t.Fatalf("applyPromptDefaults: %v", err)
	}
	if req.Name != "gpt-5.6-sol" || req.Backend != api.BackendCodexAgent || req.Effort != api.EffortHigh {
		t.Fatalf("request = %+v, want selector model/effort", req.Model)
	}
	if cfg.Model.Effort != api.EffortHigh {
		t.Fatalf("config effort = %q, want high", cfg.Model.Effort)
	}
}

func isolateCaptainConfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".captain.yaml")
	captainconfig.SetPathForTesting(path)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
}
