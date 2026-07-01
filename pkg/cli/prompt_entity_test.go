package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
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
	if rendered.Input.Context.Dir == "" || !filepath.IsAbs(rendered.Input.Context.Dir) {
		t.Fatalf("rendered context dir = %q, want absolute", rendered.Input.Context.Dir)
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
		Runtime: PromptRuntimeOptions{
			Spec: &api.Spec{
				Model: api.Model{
					Name:        "gpt-4o",
					ID:          "openai/gpt-4o",
					Backend:     api.BackendOpenAI,
					Temperature: &temp,
					Effort:      api.EffortLow,
				},
				Prompt: api.Prompt{
					System:       "runtime system",
					AppendSystem: "runtime append",
					Source:       "runtime-source",
					Metadata:     map[string]string{"surface": "prompt-ui"},
				},
				Budget: api.Budget{Cost: 0.5, MaxTokens: 1234},
				Permissions: api.Permissions{
					Mode:    api.PermissionAcceptEdits,
					Presets: []api.Preset{api.PresetEdit},
					Tools: api.Tools{
						Allow: []string{"Read"},
						Deny:  []string{"Bash"},
						Modes: map[string]api.ToolMode{"Bash": api.ToolModeDisabled},
					},
					MCP:     api.MCP{Disabled: true, Servers: []string{"filesystem"}},
					Plugins: []string{"/plugins"},
				},
				Memory: api.Memory{
					Skills:     []string{"/skills"},
					SkipUser:   true,
					SkipMemory: true,
					Bare:       true,
				},
				Context: api.Context{
					Dir:   "workspace",
					Files: []string{"a.go"},
					Git:   &api.Git{Repo: "/repo", SHA: "abc123", PR: "42"},
					Worktree: &api.Worktree{
						Branch:     "runtime-branch",
						KeepOnExit: true,
					},
					Env: map[string]string{"CAPTAIN_UI": "1"},
				},
				SessionID: "sess-runtime",
				MaxTurns:  4,
			},
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
	if rendered.Input.Budget.Cost != 0.5 || rendered.Input.Budget.MaxTokens != 1234 {
		t.Fatalf("budget = %+v, want cost/maxTokens override", rendered.Input.Budget)
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
	if !rendered.Input.Memory.SkipUser || !rendered.Input.Memory.SkipMemory || !rendered.Input.Memory.Bare {
		t.Fatalf("memory = %+v, want runtime overrides", rendered.Input.Memory)
	}
	if rendered.Input.Context.Dir != filepath.Join(cwd, "workspace") {
		t.Fatalf("context dir = %q, want cwd-relative runtime dir", rendered.Input.Context.Dir)
	}
	if rendered.Input.Context.Git == nil || rendered.Input.Context.Git.SHA != "abc123" {
		t.Fatalf("git context = %+v, want runtime git overlay", rendered.Input.Context.Git)
	}
	if rendered.Input.Context.Worktree == nil || !rendered.Input.Context.Worktree.KeepOnExit {
		t.Fatalf("worktree context = %+v, want runtime worktree overlay", rendered.Input.Context.Worktree)
	}
	if rendered.Input.Context.Env["CAPTAIN_UI"] != "1" || rendered.Input.SessionID != "sess-runtime" || rendered.Input.MaxTurns != 4 {
		t.Fatalf("runtime tail fields = input=%+v context=%+v", rendered.Input, rendered.Input.Context)
	}
}

func isolateCaptainConfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".captain.yaml")
	captainconfig.SetPathForTesting(path)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
}
