package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

func TestParseVars(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    map[string]any
		wantErr bool
	}{
		{"empty", nil, map[string]any{}, false},
		{"simple", []string{"lang=go"}, map[string]any{"lang": "go"}, false},
		{"value has equals", []string{"q=a=b"}, map[string]any{"q": "a=b"}, false},
		{"multiple", []string{"a=1", "b=2"}, map[string]any{"a": "1", "b": "2"}, false},
		{"missing equals", []string{"oops"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVars(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseVars(%v) = nil error, want error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVars(%v): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseVars(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func writePromptFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "p.prompt")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
	return p
}

func TestResolvePromptTemplate(t *testing.T) {
	file := writePromptFile(t, "{{role \"user\"}}\nfrom file")

	t.Run("positional file", func(t *testing.T) {
		tmpl, usedStdin, err := resolvePromptTemplate(AIPromptOptions{File: file}, "ignored stdin")
		if err != nil {
			t.Fatal(err)
		}
		if usedStdin {
			t.Error("usedStdin = true, want false when a file is the source")
		}
		req, _, err := tmpl.Render(prompt.RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if req.Prompt.User != "from file" {
			t.Errorf("User = %q, want %q", req.Prompt.User, "from file")
		}
	})

	t.Run("literal prompt", func(t *testing.T) {
		opts := AIPromptOptions{}
		opts.Prompt = "literal text"
		tmpl, usedStdin, err := resolvePromptTemplate(opts, "ignored")
		if err != nil {
			t.Fatal(err)
		}
		if usedStdin {
			t.Error("usedStdin = true, want false when --prompt is the source")
		}
		req, _, _ := tmpl.Render(prompt.RenderOptions{})
		if req.Prompt.User != "literal text" {
			t.Errorf("User = %q, want %q", req.Prompt.User, "literal text")
		}
	})

	t.Run("stdin source", func(t *testing.T) {
		tmpl, usedStdin, err := resolvePromptTemplate(AIPromptOptions{}, "from stdin")
		if err != nil {
			t.Fatal(err)
		}
		if !usedStdin {
			t.Error("usedStdin = false, want true when stdin is the source")
		}
		req, _, _ := tmpl.Render(prompt.RenderOptions{})
		if req.Prompt.User != "from stdin" {
			t.Errorf("User = %q, want %q", req.Prompt.User, "from stdin")
		}
	})

	t.Run("no source", func(t *testing.T) {
		if _, _, err := resolvePromptTemplate(AIPromptOptions{}, "   "); err == nil {
			t.Fatal("expected error when no prompt source is provided")
		}
	})
}

// baseFileReq is a spec as it would come back from rendering a .prompt file with
// frontmatter, used as the merge base in overlay tests.
func baseFileReq() ai.Request {
	return ai.Request{
		Prompt:      api.Prompt{User: "body prompt"},
		Model:       api.Model{Name: "claude-sonnet-5"},
		Budget:      api.Budget{MaxTokens: 100},
		Sandbox:     &api.SandboxRef{Mode: api.SandboxNative},
		Permissions: api.Permissions{Mode: api.PermissionAcceptEdits},
		Memory:      api.Memory{SkipUser: true},
	}
}

func TestResolveCLI_CLIOverridesFrontmatter(t *testing.T) {
	isolateSavedAI(t)
	opts := AIPromptOptions{}
	opts.Model = "claude-opus-4-8"
	opts.MaxTokens = 200
	opts.PermissionMode = "plan"

	req, cfg, err := runtimeLayersForTest(baseFileReq(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if req.Model.Name != "claude-opus-4-8" {
		t.Errorf("Model.Name = %q, want CLI value claude-opus-4-8", req.Model.Name)
	}
	if cfg.Model.Name != "claude-opus-4-8" {
		t.Errorf("cfg.Model.Name = %q, want CLI value mirrored into config", cfg.Model.Name)
	}
	if req.Budget.MaxTokens != 200 {
		t.Errorf("MaxTokens = %d, want CLI value 200", req.Budget.MaxTokens)
	}
	if req.Sandbox == nil || req.Permissions.Mode != api.PermissionPlan {
		t.Errorf("Sandbox approval = %#v, want CLI value plan", req.Sandbox)
	}
}

func TestResolveCLI_Sandbox(t *testing.T) {
	for _, tt := range []struct{ name, mode, baseMode, wantErr string }{
		{name: "selects CLI for frontmatter model"},
		{name: "rejects explicit API mode", mode: "api", wantErr: "requires cli mode"},
		{name: "rejects authored API mode", baseMode: "api", wantErr: "requires cli mode"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateSavedAI(t)
			base := baseFileReq()
			base.Model.Provider = api.Anthropic
			base.Model.Mode = api.RuntimeMode(tt.baseMode)
			opts := AIPromptOptions{AIRuntimeOptions: AIRuntimeOptions{AIProviderOptions: AIProviderOptions{Sandbox: "docker"}}}
			opts.Mode = tt.mode
			req, cfg, err := runtimeLayersForTest(base, opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if req.Model.Provider != api.Anthropic || req.Model.Mode != api.ModeCLI || req.Model.Name != base.Model.Name ||
				cfg.SandboxSelection == nil || cfg.SandboxSelection.Kind != api.SandboxDocker {
				t.Fatalf("sandbox model = %#v, selection=%v", req.Model, cfg.SandboxSelection)
			}
		})
	}
}

// An explicit --sandbox=off must turn off a sandbox the base config carried:
// the overlay resolved every layer, so it overwrites instead of ORing.
func TestResolveCLI_SandboxOffClearsInherited(t *testing.T) {
	isolateSavedAI(t)
	base := baseFileReq()
	base.Sandbox = &api.SandboxRef{Mode: api.SandboxDocker}
	opts := AIPromptOptions{AIRuntimeOptions: AIRuntimeOptions{AIProviderOptions: AIProviderOptions{Sandbox: "off"}}}

	_, cfg, err := runtimeLayersForTest(base, opts)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SandboxSelection != nil {
		t.Fatalf("selection=%v, want cleared by explicit off", cfg.SandboxSelection)
	}
	if resolved := cfg.ResolvedSandbox(); resolved != nil {
		t.Fatalf("ResolvedSandbox = %+v, want nil", resolved)
	}
}

func TestResolveCLI_SandboxFlagPreservesFrontmatterAgentAndPolicy(t *testing.T) {
	isolateSavedAI(t)
	base := baseFileReq()
	base.Sandbox = &api.SandboxRef{
		Mode:     api.SandboxGitAgent,
		Backend:  "old-pool",
		Agent:    "worker-01",
		Dispatch: &api.SandboxDispatchPolicy{Paths: []string{"pkg/**"}, MaxAttempts: 3},
	}
	opts := AIPromptOptions{AIRuntimeOptions: AIRuntimeOptions{AIProviderOptions: AIProviderOptions{Sandbox: "git-agent"}}}

	req, cfg, err := runtimeLayersForTest(base, opts)
	if err != nil {
		t.Fatal(err)
	}
	if req.Sandbox == nil || req.Sandbox.Mode != api.SandboxGitAgent || req.Sandbox.Backend != "" || req.Sandbox.Agent != "worker-01" {
		t.Fatalf("request sandbox = %#v", req.Sandbox)
	}
	if req.Sandbox.Dispatch == nil || req.Sandbox.Dispatch.MaxAttempts != 3 {
		t.Fatalf("request dispatch = %#v", req.Sandbox.Dispatch)
	}
	if cfg.SandboxSelection == nil || cfg.SandboxSelection.Agent != "worker-01" || cfg.SandboxSelection.Dispatch != req.Sandbox.Dispatch {
		t.Fatalf("config sandbox = %#v", cfg.SandboxSelection)
	}
}

func TestResolveCLI_APIURLTransport(t *testing.T) {
	isolateSavedAI(t)
	opts := AIPromptOptions{}
	opts.APIURL = "http://127.0.0.1:18096/v1"

	_, cfg, err := runtimeLayersForTest(baseFileReq(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIURL != opts.APIURL {
		t.Errorf("cfg.APIURL = %q, want CLI value %q", cfg.APIURL, opts.APIURL)
	}

	_, cfg, err = runtimeLayersForTest(baseFileReq(), AIPromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIURL != "" {
		t.Errorf("cfg.APIURL = %q, want no endpoint without an explicit flag", cfg.APIURL)
	}
}

func TestResolveCLI_FrontmatterOverridesSaved(t *testing.T) {
	seedSavedAI(t, "ai:\n  defaultModel: agent:claude-haiku-4-5\n  maxTokens: 16000\n")
	req, _, err := runtimeLayersForTest(baseFileReq(), AIPromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if req.Model.Name != "claude-sonnet-5" {
		t.Errorf("Model.Name = %q, want frontmatter value claude-sonnet-5 (beats saved)", req.Model.Name)
	}
	if req.Budget.MaxTokens != 100 {
		t.Errorf("MaxTokens = %d, want frontmatter value 100 (beats saved 16000)", req.Budget.MaxTokens)
	}
}

func TestResolveCLI_BuiltinMaxTokens(t *testing.T) {
	isolateSavedAI(t)
	base := baseFileReq()
	base.Budget.MaxTokens = 0 // frontmatter omitted it
	req, _, err := runtimeLayersForTest(base, AIPromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if req.Budget.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want built-in 4096", req.Budget.MaxTokens)
	}
}

func TestResolveCLI_OmittedBooleansInherit(t *testing.T) {
	isolateSavedAI(t)
	opts := AIPromptOptions{}
	opts.NoMCP = true   // CLI sets it
	opts.Edit = true    // CLI preset
	opts.NoUser = false // base already has SkipUser=true

	req, _, err := runtimeLayersForTest(baseFileReq(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !req.Permissions.MCP.Disabled {
		t.Error("MCP.Disabled = false, want true (CLI --no-mcp)")
	}
	if !req.Memory.SkipUser {
		t.Error("SkipUser = false, want true (frontmatter set it; omitted CLI flag must not clear it)")
	}
	if !req.Permissions.HasPreset(api.PresetEdit) {
		t.Error("missing edit preset from --edit")
	}
	// --edit must not duplicate a preset the frontmatter already declared.
	base := baseFileReq()
	base.Permissions.Presets = []api.Preset{api.PresetEdit}
	req2, _, err := runtimeLayersForTest(base, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(req2.Permissions.Presets); got != 1 {
		t.Errorf("presets = %d, want 1 (no duplicate edit)", got)
	}
}

func fallbackNames(models []api.Model) []string {
	if len(models) == 0 {
		return nil
	}
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.Name
	}
	return out
}

func TestResolveCLI_ModelCSVExpandsToFallbacks(t *testing.T) {
	isolateSavedAI(t)
	opts := AIPromptOptions{}
	opts.Sandbox = "off"
	opts.Model = "claude-sonnet-5,gpt-4o,gemini-2.0-flash"

	req, cfg, err := runtimeLayersForTest(baseFileReq(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if req.Model.Name != "claude-sonnet-5" {
		t.Errorf("Model.Name = %q, want CSV head claude-sonnet-5", req.Model.Name)
	}
	if got := fallbackNames(req.Model.Fallbacks); !reflect.DeepEqual(got, []string{"gpt-4o", "gemini-2.0-flash"}) {
		t.Errorf("req fallbacks = %v, want [gpt-4o gemini-2.0-flash]", got)
	}
	if got := fallbackNames(cfg.Model.Fallbacks); !reflect.DeepEqual(got, []string{"gpt-4o", "gemini-2.0-flash"}) {
		t.Errorf("cfg fallbacks = %v, want mirrored into config", got)
	}
}

func TestResolveCLI_FallbackFlagOverridesFrontmatter(t *testing.T) {
	isolateSavedAI(t)
	base := baseFileReq()
	base.Model.Fallbacks = []api.Model{{Name: "frontmatter-fallback"}}
	opts := AIPromptOptions{}
	opts.Sandbox = "off"
	opts.Fallback = []string{"gemini-3.5-flash", "gpt-5.5,claude-sonnet-5"} // repeatable + comma-split

	req, _, err := runtimeLayersForTest(base, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gemini-3.5-flash", "gpt-5.5", "claude-sonnet-5"}
	if got := fallbackNames(req.Model.Fallbacks); !reflect.DeepEqual(got, want) {
		t.Errorf("fallbacks = %v, want CLI flags %v (override frontmatter)", got, want)
	}
}

func TestResolveCLI_FrontmatterFallbacksStandWithoutFlag(t *testing.T) {
	isolateSavedAI(t)
	base := baseFileReq()
	base.Model.Fallbacks = []api.Model{{Name: "gpt-5.6-sol", Effort: api.EffortHigh}}

	req, _, err := runtimeLayersForTest(base, AIPromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := fallbackNames(req.Model.Fallbacks); !reflect.DeepEqual(got, []string{"gpt-5.6-sol"}) {
		t.Errorf("fallbacks = %v, want frontmatter [gpt-5.6-sol] when no --fallback", got)
	}
	if req.Model.Fallbacks[0].Effort != api.EffortHigh {
		t.Errorf("frontmatter fallback effort = %q, want preserved high", req.Model.Fallbacks[0].Effort)
	}
}

func TestNormalizePromptContextDir(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	abs := filepath.Join(t.TempDir(), "other", "..", "other")

	tests := []struct {
		name string
		dir  string
		want string
	}{
		{name: "missing defaults to cwd", want: cwd},
		{name: "dot resolves against cwd", dir: ".", want: cwd},
		{name: "relative resolves against cwd", dir: "sub/../work", want: filepath.Join(cwd, "work")},
		{name: "absolute is preserved and cleaned", dir: abs, want: filepath.Clean(abs)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := ai.Request{Setup: &shell.Setup{Cwd: tc.dir}}
			if err := normalizePromptContextDir(&req, cwd); err != nil {
				t.Fatalf("normalizePromptContextDir: %v", err)
			}
			if req.Cwd() != tc.want {
				t.Errorf("Setup.Cwd = %q, want %q", req.Cwd(), tc.want)
			}
		})
	}
}
