package cli

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
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
prompt:
  attachments:
    - path: frontmatter.png
permissions:
  tools:
    Edit: allow
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
				Name:        "claude-sonnet-5",
				ID:          "anthropic/claude-sonnet-5",
				Provider:    api.Anthropic,
				Mode:        api.ModeAgent,
				Temperature: &temp,
				Effort:      api.EffortLow,
				NoCache:     true,
			},
			Prompt: api.Prompt{
				System:       "runtime system",
				AppendSystem: "runtime append",
				Source:       "runtime-source",
				Metadata:     map[string]string{"surface": "prompt-ui"},
				Attachments:  []api.AttachmentRef{{Path: "request.png"}},
			},
			Budget: api.Budget{Cost: 0.5, MaxTokens: 1234, MaxTurns: 4, Timeout: "90s"},
			Permissions: api.Permissions{
				Mode:    api.PermissionAcceptEdits,
				Presets: []api.Preset{api.PresetEdit},
				Tools: api.Tools{
					"Read": api.ToolPolicyAllow,
					"Bash": api.ToolPolicyDeny,
				},
				MCP: api.MCP{
					Disabled: true,
					Servers:  []string{"filesystem"},
					Modes:    api.ResourcePolicies{"gavel": api.ResourceDisabled},
				},
				Plugins: api.ResourcePolicies{"/plugins": api.ResourceEnabled},
				Skills:  api.ResourcePolicies{"/permission-skills": api.ResourceEnabled},
			},
			Sandbox: &api.SandboxRef{Mode: api.SandboxNative},
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
	if rendered.Model != "claude-sonnet-5" || rendered.Provider != "anthropic" || rendered.Mode != "agent" {
		t.Fatalf("rendered model/runtime = %s %s/%s, want anthropic agent/claude-sonnet-5", rendered.Provider, rendered.Mode, rendered.Model)
	}
	if rendered.Config.Model.ID != "anthropic/claude-sonnet-5" {
		t.Fatalf("config model ID = %q, want anthropic/claude-sonnet-5", rendered.Config.Model.ID)
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
	if rendered.Input.Sandbox == nil || rendered.Input.Permissions.Mode != api.PermissionAcceptEdits ||
		rendered.Input.Permissions.Tools["Bash"] != api.ToolPolicyDeny ||
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
	wantTools := api.Tools{"Edit": api.ToolPolicyAllow, "Read": api.ToolPolicyAllow, "Bash": api.ToolPolicyDeny}
	if !reflect.DeepEqual(rendered.Input.Permissions.Tools, wantTools) {
		t.Fatalf("tools = %+v, want frontmatter and request policies composed key-wise %+v", rendered.Input.Permissions.Tools, wantTools)
	}
	wantAttachments := []api.AttachmentRef{{Path: "request.png"}}
	if !reflect.DeepEqual(rendered.Input.Prompt.Attachments, wantAttachments) {
		t.Fatalf("attachments = %+v, want the request's to replace the frontmatter's", rendered.Input.Prompt.Attachments)
	}
	var sources []api.SpecLayerSource
	for _, layer := range rendered.Resolution.Trace {
		sources = append(sources, layer.Source)
	}
	if !reflect.DeepEqual(sources, []api.SpecLayerSource{api.SpecLayerSourcePrompt, api.SpecLayerSourceRequest}) {
		t.Fatalf("trace sources = %v, want [prompt request]", sources)
	}
	if rendered.Resolution.Trace[0].Name != created.RelPath || rendered.Resolution.Trace[1].Name != "render request" {
		t.Fatalf("trace names = %q %q, want the prompt path and the render request", rendered.Resolution.Trace[0].Name, rendered.Resolution.Trace[1].Name)
	}
	if !reflect.DeepEqual(rendered.Resolution.Spec, rendered.Input) {
		t.Fatalf("resolution spec = %+v, want the final input after defaults", rendered.Resolution.Spec)
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
				Provider:    api.OpenAI,
				Mode:        api.ModeAgent,
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
	if rendered.Model != "gpt-5.5" || rendered.Provider != "openai" || rendered.Mode != "agent" {
		t.Fatalf("rendered model/runtime = %s %s/%s, want openai agent/gpt-5.5", rendered.Provider, rendered.Mode, rendered.Model)
	}
	if rendered.Input.Prompt.Source != "<ephemeral>" {
		t.Fatalf("prompt source = %q, want <ephemeral>", rendered.Input.Prompt.Source)
	}
	if rendered.Input.Cwd() != cwd {
		t.Fatalf("setup cwd = %q, want %q", rendered.Input.Cwd(), cwd)
	}
}

// TestRenderPromptResolvesSandbox pins the sandbox contract of the HTTP/Spec
// render path. Sandbox resolution used to be reachable only from overlayCLI, so
// a run submitted over HTTP dropped both the prompt's own `sandbox:` frontmatter
// and the configured sandbox.default and executed unconfined — silently, because
// the fail-loud guards only fire once a selection exists.
func TestRenderPromptResolvesSandbox(t *testing.T) {
	tests := []struct {
		name          string
		frontmatter   string
		globalDefault string
		override      *api.SandboxRef
		want          registry.SandboxKind
	}{
		{
			name:        "frontmatter selects the sandbox",
			frontmatter: "sandbox: docker\n",
			want:        registry.SandboxDocker,
		},
		{
			name:          "global default applies when the prompt selects none",
			globalDefault: "docker",
			want:          registry.SandboxDocker,
		},
		{
			name:          "frontmatter beats the global default",
			frontmatter:   "sandbox: off\n",
			globalDefault: "docker",
			want:          registry.SandboxOff,
		},
		{
			name:          "request spec beats both",
			frontmatter:   "sandbox: off\n",
			globalDefault: "off",
			override:      &api.SandboxRef{Mode: api.SandboxDocker},
			want:          registry.SandboxDocker,
		},
		{
			name: "nothing selects anything",
			want: registry.SandboxOff,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateCaptainConfig(t)
			if tt.globalDefault != "" {
				if err := captainconfig.Save(captainconfig.Config{
					Sandbox: captainconfig.SandboxDefaults{Default: tt.globalDefault},
				}); err != nil {
					t.Fatalf("save captain config: %v", err)
				}
			}

			dir := t.TempDir()
			t.Chdir(t.TempDir())
			ctx := ContextWithPromptDirs(context.Background(), []string{dir})
			created, err := createPrompt(ctx, map[string]any{
				"name": "Sandboxed",
				"content": "---\nname: Sandboxed\nmodel: claude-code-opus\n" + tt.frontmatter +
					"---\n{{role \"user\"}}\nHello\n",
			})
			if err != nil {
				t.Fatalf("createPrompt() err = %v", err)
			}

			rendered, err := renderPrompt(ctx, created.ID, PromptRenderRequest{
				Spec: &api.Spec{Sandbox: tt.override},
			})
			if err != nil {
				t.Fatalf("renderPrompt() err = %v", err)
			}
			if rendered.ValidationError != "" {
				t.Fatalf("render validation error = %q", rendered.ValidationError)
			}

			got := registry.SandboxOff
			if selection := rendered.Config.ResolvedSandbox(); selection != nil {
				got = selection.Kind
			}
			if got != tt.want {
				t.Fatalf("resolved sandbox = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRenderPromptPreservesSandboxMetadata pins the rest of the ref. Resolution
// only ever consumed SandboxRef.Backend, so a spec that also pinned an agent or
// a per-run policy could resolve to the right kind while silently dropping the
// two fields that bound what the dispatched run may touch.
func TestRenderPromptPreservesSandboxMetadata(t *testing.T) {
	isolateCaptainConfig(t)
	dir := t.TempDir()
	t.Chdir(t.TempDir())
	ctx := ContextWithPromptDirs(context.Background(), []string{dir})
	created, err := createPrompt(ctx, map[string]any{
		"name":    "Sandboxed",
		"content": "---\nname: Sandboxed\nmodel: claude-code-opus\nsandbox: off\n---\n{{role \"user\"}}\nHello\n",
	})
	if err != nil {
		t.Fatalf("createPrompt() err = %v", err)
	}

	policy := &api.SandboxDispatchPolicy{Paths: []string{"pkg/**"}, MaxAttempts: 3}
	rendered, err := renderPrompt(ctx, created.ID, PromptRenderRequest{
		Spec: &api.Spec{Sandbox: &api.SandboxRef{Mode: api.SandboxGitAgent, Agent: "builder-1", Dispatch: policy}},
	})
	if err != nil {
		t.Fatalf("renderPrompt() err = %v", err)
	}
	if rendered.ValidationError != "" {
		t.Fatalf("render validation error = %q", rendered.ValidationError)
	}

	if rendered.Input.Sandbox == nil {
		t.Fatal("request sandbox ref = nil, want the spec's ref")
	}
	if rendered.Input.Sandbox.Agent != "builder-1" {
		t.Errorf("request sandbox agent = %q, want builder-1", rendered.Input.Sandbox.Agent)
	}
	if !reflect.DeepEqual(rendered.Input.Sandbox.Dispatch, policy) {
		t.Errorf("request sandbox dispatch = %+v, want %+v", rendered.Input.Sandbox.Dispatch, policy)
	}

	selection := rendered.Config.ResolvedSandbox()
	if selection == nil {
		t.Fatal("resolved sandbox = nil, want the git-agent selection")
	}
	if selection.Kind != registry.SandboxGitAgent {
		t.Errorf("resolved sandbox kind = %q, want %q", selection.Kind, registry.SandboxGitAgent)
	}
	if selection.Agent != "builder-1" {
		t.Errorf("resolved sandbox agent = %q, want builder-1", selection.Agent)
	}
	if !reflect.DeepEqual(selection.Dispatch, policy) {
		t.Errorf("resolved sandbox dispatch = %+v, want %+v", selection.Dispatch, policy)
	}
}

// TestRenderPromptRunnability pins render-time agreement with
// api.Spec.ValidateRunnable, which promptrun.Run enforces. Render used to accept
// an attachments-only prompt ("prompt text or attachment required" fired only
// when both were absent), so `captain prompt run` built a provider and opened a
// stream before the run seam rejected the very same spec — the failure arrived
// after the expensive part, with a different message.
func TestRenderPromptRunnability(t *testing.T) {
	notRunnable := api.Spec{}.ValidateRunnable().Error()

	tests := []struct {
		name        string
		frontmatter string
		body        string
		want        string
	}{
		{
			name:        "attachments without a prompt body or verify are not a run",
			frontmatter: "prompt:\n  attachments:\n    - path: diagram.png\n",
			want:        notRunnable,
		},
		{
			// api.Spec.IsVerifyOnly requires no attachments: attachments are
			// input to a generation, so a spec carrying them but no instruction
			// is under-specified even with a verify declared. Render must give
			// the same answer promptrun.Run does, not a friendlier one.
			name:        "attachments plus workflow.verify are still not a run",
			frontmatter: "prompt:\n  attachments:\n    - path: diagram.png\n" + verifyFrontmatter,
			want:        notRunnable,
		},
		{
			name: "a prompt body is runnable on its own",
			body: "Summarize the diagram\n",
			want: "",
		},
		{
			name:        "an empty prompt with workflow.verify is verify-only",
			frontmatter: verifyFrontmatter,
			want:        "",
		},
		{
			name: "an empty prompt with neither is not a run",
			want: notRunnable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateCaptainConfig(t)
			dir := t.TempDir()
			t.Chdir(t.TempDir())
			ctx := ContextWithPromptDirs(context.Background(), []string{dir})
			created, err := createPrompt(ctx, map[string]any{
				"name": "Runnable",
				"content": "---\nname: Runnable\nmodel: claude-sonnet-4-6\n" + tt.frontmatter +
					"---\n{{role \"user\"}}\n" + tt.body,
			})
			if err != nil {
				t.Fatalf("createPrompt() err = %v", err)
			}

			rendered, err := renderPrompt(ctx, created.ID, PromptRenderRequest{})
			if err != nil {
				t.Fatalf("renderPrompt() err = %v", err)
			}
			if rendered.ValidationError != tt.want {
				t.Fatalf("validation error = %q, want %q", rendered.ValidationError, tt.want)
			}
			if tt.want == "" && tt.body == "" && !rendered.Input.IsVerifyOnly() {
				t.Fatalf("spec %+v is not verify-only; render accepted a body-less prompt for another reason", rendered.Input.Prompt)
			}
		})
	}
}

const verifyFrontmatter = "workflow:\n  verify:\n    commands:\n      - \"true\"\n"

func TestResolvePromptSelectorEffortWins(t *testing.T) {
	isolateCaptainConfig(t)
	resolved, err := resolveInvocation(AIRuntimeOptions{}, []api.SpecLayer{
		api.PromptSpecLayer("template", api.Spec{Model: api.Model{Effort: api.EffortLow}}),
		api.RequestSpecLayer("request", api.Spec{Model: api.Model{Name: "gpt-5.6-sol", Mode: api.ModeAgent, Effort: api.EffortHigh}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, cfg := resolved.Request, resolved.Config
	if req.Name != "gpt-5.6-sol" || req.Mode != api.ModeAgent || req.Effort != api.EffortHigh {
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
