package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
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
		req, _, err := tmpl.Render(nil, nil)
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
		req, _, _ := tmpl.Render(nil, nil)
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
		req, _, _ := tmpl.Render(nil, nil)
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
		Model:       api.Model{Name: "claude-file-4-6"},
		Budget:      api.Budget{MaxTokens: 100},
		Permissions: api.Permissions{Mode: api.PermissionAcceptEdits},
		Memory:      api.Memory{SkipUser: true},
	}
}

func TestOverlayCLI_CLIOverridesFrontmatter(t *testing.T) {
	isolateSavedAI(t)
	opts := AIPromptOptions{}
	opts.Model = "claude-cli-4-6"
	opts.MaxTokens = 200
	opts.PermissionMode = "plan"

	req, cfg, err := overlayCLI(baseFileReq(), ai.Config{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if req.Model.Name != "claude-cli-4-6" {
		t.Errorf("Model.Name = %q, want CLI value claude-cli-4-6", req.Model.Name)
	}
	if cfg.Model.Name != "claude-cli-4-6" {
		t.Errorf("cfg.Model.Name = %q, want CLI value mirrored into config", cfg.Model.Name)
	}
	if req.Budget.MaxTokens != 200 {
		t.Errorf("MaxTokens = %d, want CLI value 200", req.Budget.MaxTokens)
	}
	if req.Permissions.Mode != api.PermissionPlan {
		t.Errorf("Mode = %q, want CLI value plan", req.Permissions.Mode)
	}
}

func TestOverlayCLI_SandboxForcesFrontmatterModelToCLI(t *testing.T) {
	isolateSavedAI(t)
	req, cfg, err := overlayCLI(baseFileReq(), ai.Config{}, AIPromptOptions{
		AIRuntimeOptions: AIRuntimeOptions{
			AIProviderOptions: AIProviderOptions{Sandbox: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Model.Backend != api.BackendClaudeCLI {
		t.Fatalf("req backend = %q, want %q", req.Model.Backend, api.BackendClaudeCLI)
	}
	if !cfg.Sandbox {
		t.Fatal("cfg.Sandbox = false, want true")
	}
}

// --api-url is what points a run at a `captain ai mock` endpoint, so it has to
// survive the overlay; a prompt file may pin its own endpoint, and the flag wins.
func TestOverlayCLI_APIURLFlagBeatsFrontmatter(t *testing.T) {
	isolateSavedAI(t)
	opts := AIPromptOptions{}
	opts.APIURL = "http://127.0.0.1:18096/v1"

	_, cfg, err := overlayCLI(baseFileReq(), ai.Config{APIURL: "https://api.openai.com/v1"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIURL != opts.APIURL {
		t.Errorf("cfg.APIURL = %q, want CLI value %q", cfg.APIURL, opts.APIURL)
	}

	_, cfg, err = overlayCLI(baseFileReq(), ai.Config{APIURL: "https://api.openai.com/v1"}, AIPromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIURL != "https://api.openai.com/v1" {
		t.Errorf("cfg.APIURL = %q, want the frontmatter value to stand without a flag", cfg.APIURL)
	}
}

func TestOverlayCLI_FrontmatterOverridesSaved(t *testing.T) {
	seedSavedAI(t, "ai:\n  model: claude-saved-4-6\n  maxTokens: 16000\n")
	req, _, err := overlayCLI(baseFileReq(), ai.Config{}, AIPromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if req.Model.Name != "claude-file-4-6" {
		t.Errorf("Model.Name = %q, want frontmatter value claude-file-4-6 (beats saved)", req.Model.Name)
	}
	if req.Budget.MaxTokens != 100 {
		t.Errorf("MaxTokens = %d, want frontmatter value 100 (beats saved 16000)", req.Budget.MaxTokens)
	}
}

func TestOverlayCLI_BuiltinMaxTokens(t *testing.T) {
	isolateSavedAI(t)
	base := baseFileReq()
	base.Budget.MaxTokens = 0 // frontmatter omitted it
	req, _, err := overlayCLI(base, ai.Config{}, AIPromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if req.Budget.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want built-in 4096", req.Budget.MaxTokens)
	}
}

func TestOverlayCLI_BooleansOR(t *testing.T) {
	isolateSavedAI(t)
	opts := AIPromptOptions{}
	opts.NoMCP = true   // CLI sets it
	opts.Edit = true    // CLI preset
	opts.NoUser = false // base already has SkipUser=true

	req, _, err := overlayCLI(baseFileReq(), ai.Config{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !req.Permissions.MCP.Disabled {
		t.Error("MCP.Disabled = false, want true (CLI --no-mcp)")
	}
	if !req.Memory.SkipUser {
		t.Error("SkipUser = false, want true (frontmatter set it; CLI false must not clear it)")
	}
	if !req.Permissions.HasPreset(api.PresetEdit) {
		t.Error("missing edit preset from --edit")
	}
	// --edit must not duplicate a preset the frontmatter already declared.
	base := baseFileReq()
	base.Permissions.Presets = []api.Preset{api.PresetEdit}
	req2, _, err := overlayCLI(base, ai.Config{}, opts)
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

func TestOverlayCLI_ModelCSVExpandsToFallbacks(t *testing.T) {
	isolateSavedAI(t)
	opts := AIPromptOptions{}
	opts.Model = "claude-primary-5,gpt-4o,gemini-2.0-flash"

	req, cfg, err := overlayCLI(baseFileReq(), ai.Config{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if req.Model.Name != "claude-primary-5" {
		t.Errorf("Model.Name = %q, want CSV head claude-primary-5", req.Model.Name)
	}
	if got := fallbackNames(req.Model.Fallbacks); !reflect.DeepEqual(got, []string{"gpt-4o", "gemini-2.0-flash"}) {
		t.Errorf("req fallbacks = %v, want [gpt-4o gemini-2.0-flash]", got)
	}
	if got := fallbackNames(cfg.Model.Fallbacks); !reflect.DeepEqual(got, []string{"gpt-4o", "gemini-2.0-flash"}) {
		t.Errorf("cfg fallbacks = %v, want mirrored into config", got)
	}
}

func TestOverlayCLI_FallbackFlagOverridesFrontmatter(t *testing.T) {
	isolateSavedAI(t)
	base := baseFileReq()
	base.Model.Fallbacks = []api.Model{{Name: "frontmatter-fallback"}}
	opts := AIPromptOptions{}
	opts.Fallback = []string{"gemini-3.5-flash", "gpt-5.5,claude-sonnet-5"} // repeatable + comma-split

	req, _, err := overlayCLI(base, ai.Config{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gemini-3.5-flash", "gpt-5.5", "claude-sonnet-5"}
	if got := fallbackNames(req.Model.Fallbacks); !reflect.DeepEqual(got, want) {
		t.Errorf("fallbacks = %v, want CLI flags %v (override frontmatter)", got, want)
	}
}

func TestOverlayCLI_FrontmatterFallbacksStandWithoutFlag(t *testing.T) {
	isolateSavedAI(t)
	base := baseFileReq()
	base.Model.Fallbacks = []api.Model{{Name: "gpt-4o", Effort: api.EffortHigh}}

	req, _, err := overlayCLI(base, ai.Config{}, AIPromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := fallbackNames(req.Model.Fallbacks); !reflect.DeepEqual(got, []string{"gpt-4o"}) {
		t.Errorf("fallbacks = %v, want frontmatter [gpt-4o] when no --fallback", got)
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
