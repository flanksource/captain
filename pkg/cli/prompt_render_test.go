package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/commons-db/shell"
)

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
					Modes: map[string]api.ToolMode{"Bash": api.ToolModeOff},
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
		rendered.Input.Permissions.Tools.Modes["Bash"] != api.ToolModeOff ||
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
