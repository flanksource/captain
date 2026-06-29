package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// isolateSavedAI redirects captainconfig.Path() to an empty file inside
// t.TempDir() so loadSavedAI() returns zero defaults rather than leaking
// the developer's real ~/.captain.yaml into table-test expectations.
func isolateSavedAI(t *testing.T) {
	t.Helper()
	seedSavedAI(t, "")
}

// seedSavedAI redirects captainconfig.Path() to a temp file containing yaml.
func seedSavedAI(t *testing.T, yaml string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".captain.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatalf("seed captain config: %v", err)
	}
	captainconfig.SetPathForTesting(p)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
}

func TestAIPromptOptions_ToRequest_Defaults(t *testing.T) {
	isolateSavedAI(t)
	opts := defaultPromptOptions(t)
	req, err := opts.ToRequest()
	if err != nil {
		t.Fatalf("ToRequest: %v", err)
	}

	if req.Prompt != "hello" {
		t.Errorf("Prompt = %q, want hello", req.Prompt)
	}
	if req.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096 built-in default", req.MaxTokens)
	}
	for name, got := range map[string]bool{
		"NoMCP": req.NoMCP, "NoHooks": req.NoHooks, "NoSkills": req.NoSkills,
		"NoUser": req.NoUser, "NoProject": req.NoProject, "NoMemory": req.NoMemory,
		"Bare": req.Bare, "Edit": req.Edit,
	} {
		if got {
			t.Errorf("%s = true; default-shape options must not flip any No*/Bare/Edit", name)
		}
	}
}

// TestAIPromptOptions_ToRequest_NegativeFlags verifies each --no-* flag sets the
// matching No* field on the request.
func TestAIPromptOptions_ToRequest_NegativeFlags(t *testing.T) {
	isolateSavedAI(t)
	cases := []struct {
		name   string
		mutate func(*AIPromptOptions)
		field  string
	}{
		{"no-mcp", func(o *AIPromptOptions) { o.NoMCP = true }, "NoMCP"},
		{"no-hooks", func(o *AIPromptOptions) { o.NoHooks = true }, "NoHooks"},
		{"no-skills", func(o *AIPromptOptions) { o.NoSkills = true }, "NoSkills"},
		{"no-user", func(o *AIPromptOptions) { o.NoUser = true }, "NoUser"},
		{"no-project", func(o *AIPromptOptions) { o.NoProject = true }, "NoProject"},
		{"no-memory", func(o *AIPromptOptions) { o.NoMemory = true }, "NoMemory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaultPromptOptions(t)
			tc.mutate(&opts)
			req, err := opts.ToRequest()
			if err != nil {
				t.Fatalf("ToRequest: %v", err)
			}
			if !reflect.ValueOf(req).FieldByName(tc.field).Bool() {
				t.Errorf("%s = false, want true after --%s", tc.field, tc.name)
			}
		})
	}
}

func TestAIPromptOptions_ToRequest_PassesScalars(t *testing.T) {
	isolateSavedAI(t)
	opts := defaultPromptOptions(t)
	opts.System = "be careful"
	opts.AppendSystem = "also be brief"
	opts.PermissionMode = "acceptEdits"
	opts.Edit = true
	opts.AllowedTools = []string{"Read", "Bash"}
	opts.DisallowedTools = []string{"Write"}
	opts.SkillDirs = []string{"/skills/a", "/skills/b"}
	opts.Bare = true
	opts.MaxTokens = 1024
	opts.Temperature = "0.5"
	opts.Effort = "high"
	opts.MaxTurns = 7
	opts.Resume = "sess-123"

	req, err := opts.ToRequest()
	if err != nil {
		t.Fatalf("ToRequest: %v", err)
	}
	if req.SystemPrompt != "be careful" {
		t.Errorf("SystemPrompt = %q", req.SystemPrompt)
	}
	if req.AppendSystemPrompt != "also be brief" {
		t.Errorf("AppendSystemPrompt = %q", req.AppendSystemPrompt)
	}
	if req.PermissionMode != "acceptEdits" {
		t.Errorf("PermissionMode = %q", req.PermissionMode)
	}
	if !req.Edit {
		t.Error("Edit not propagated")
	}
	if !reflect.DeepEqual(req.AllowedTools, []string{"Read", "Bash"}) {
		t.Errorf("AllowedTools = %v", req.AllowedTools)
	}
	if !reflect.DeepEqual(req.DisallowedTools, []string{"Write"}) {
		t.Errorf("DisallowedTools = %v", req.DisallowedTools)
	}
	if !reflect.DeepEqual(req.SkillDirs, []string{"/skills/a", "/skills/b"}) {
		t.Errorf("SkillDirs = %v", req.SkillDirs)
	}
	if !req.Bare {
		t.Error("Bare not propagated")
	}
	if req.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d", req.MaxTokens)
	}
	if req.Temperature != 0.5 {
		t.Errorf("Temperature = %v", req.Temperature)
	}
	if req.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q", req.ReasoningEffort)
	}
	if req.MaxTurns != 7 {
		t.Errorf("MaxTurns = %d", req.MaxTurns)
	}
	if req.SessionID != "sess-123" {
		t.Errorf("SessionID = %q (from --resume)", req.SessionID)
	}
}

// TestAIRuntimeOptions_ToRequest_ValidationErrors verifies malformed input fails
// loudly instead of being silently coerced to a zero value.
func TestAIRuntimeOptions_ToRequest_ValidationErrors(t *testing.T) {
	isolateSavedAI(t)
	cases := []struct {
		name   string
		mutate func(*AIPromptOptions)
		want   string
	}{
		{"bad temperature", func(o *AIPromptOptions) { o.Temperature = "hot" }, "temperature"},
		{"temperature above max", func(o *AIPromptOptions) { o.Temperature = "3" }, "0.0-2.0"},
		{"temperature below min", func(o *AIPromptOptions) { o.Temperature = "-1" }, "0.0-2.0"},
		{"bad permission-mode", func(o *AIPromptOptions) { o.PermissionMode = "yolo" }, "permission-mode"},
		{"bad effort", func(o *AIPromptOptions) { o.Effort = "max" }, "effort"},
		{"max-turns above max", func(o *AIPromptOptions) { o.MaxTurns = 200 }, "max-turns"},
		{"max-turns below min", func(o *AIPromptOptions) { o.MaxTurns = -1 }, "max-turns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaultPromptOptions(t)
			tc.mutate(&opts)
			if _, err := opts.ToRequest(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestAIProviderOptions_ToConfig_ValidationErrors(t *testing.T) {
	isolateSavedAI(t)
	if _, err := (AIProviderOptions{Model: "claude-x", Backend: "nope"}).ToConfig(); err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("expected invalid backend error, got %v", err)
	}
	if _, err := (AIProviderOptions{Model: "claude-x", Budget: "free"}).ToConfig(); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected invalid budget error, got %v", err)
	}
}

// TestAIRuntimeOptions_ToRequest_MaxTokensPrecedence pins the flag > saved >
// built-in order, replacing the old magic-4096 sentinel.
func TestAIRuntimeOptions_ToRequest_MaxTokensPrecedence(t *testing.T) {
	t.Run("explicit flag wins over saved", func(t *testing.T) {
		seedSavedAI(t, "ai:\n  maxTokens: 16000\n")
		req, err := AIRuntimeOptions{MaxTokens: 2048}.ToRequest("", "", "p")
		if err != nil {
			t.Fatal(err)
		}
		if req.MaxTokens != 2048 {
			t.Errorf("MaxTokens = %d, want 2048 (explicit flag)", req.MaxTokens)
		}
	})
	t.Run("unset falls back to saved", func(t *testing.T) {
		seedSavedAI(t, "ai:\n  maxTokens: 16000\n")
		req, err := AIRuntimeOptions{}.ToRequest("", "", "p")
		if err != nil {
			t.Fatal(err)
		}
		if req.MaxTokens != 16000 {
			t.Errorf("MaxTokens = %d, want 16000 (saved)", req.MaxTokens)
		}
	})
	t.Run("unset with no saved uses built-in 4096", func(t *testing.T) {
		isolateSavedAI(t)
		req, err := AIRuntimeOptions{}.ToRequest("", "", "p")
		if err != nil {
			t.Fatal(err)
		}
		if req.MaxTokens != 4096 {
			t.Errorf("MaxTokens = %d, want 4096 (built-in)", req.MaxTokens)
		}
	})
}

// TestAIRuntimeOptions_ToRequest_OverlaysSaved verifies the path gavel (and any
// other embedder) takes: AIRuntimeOptions with zero flag values should pick up
// NoMCP/.../MaxTokens/ReasoningEffort from ~/.captain.yaml, and an explicit
// --effort flag should override the saved value.
func TestAIRuntimeOptions_ToRequest_OverlaysSaved(t *testing.T) {
	seedSavedAI(t, "ai:\n  noMCP: true\n  noHooks: true\n  noSkills: true\n  noUser: true\n  noProject: true\n  noMemory: true\n  maxTokens: 16000\n  reasoningEffort: low\n")

	opts := AIRuntimeOptions{Effort: "high"} // flag overrides saved low
	req, err := opts.ToRequest("sys", "", "user")
	if err != nil {
		t.Fatalf("ToRequest: %v", err)
	}

	if req.SystemPrompt != "sys" || req.Prompt != "user" {
		t.Errorf("prompt fields = %q/%q, want sys/user", req.SystemPrompt, req.Prompt)
	}
	if req.MaxTokens != 16000 {
		t.Errorf("MaxTokens = %d, want 16000 (saved overlay)", req.MaxTokens)
	}
	if req.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q, want high (flag overrides saved)", req.ReasoningEffort)
	}
	for name, got := range map[string]bool{
		"NoMCP": req.NoMCP, "NoHooks": req.NoHooks, "NoSkills": req.NoSkills,
		"NoUser": req.NoUser, "NoProject": req.NoProject, "NoMemory": req.NoMemory,
	} {
		if !got {
			t.Errorf("%s = false, want true (from saved config)", name)
		}
	}
}

// TestBackendHelpEnumeratesAllBackends guards the static --backend help strings
// against drift from ai.AllBackends() (the single source of truth).
func TestBackendHelpEnumeratesAllBackends(t *testing.T) {
	for _, c := range []struct {
		typ  reflect.Type
		name string
	}{
		{reflect.TypeOf(AIProviderOptions{}), "AIProviderOptions"},
		{reflect.TypeOf(AIModelsOptions{}), "AIModelsOptions"},
	} {
		f, ok := c.typ.FieldByName("Backend")
		if !ok {
			t.Fatalf("%s has no Backend field", c.name)
		}
		help := f.Tag.Get("help")
		for _, b := range ai.AllBackends() {
			if !strings.Contains(help, string(b)) {
				t.Errorf("%s Backend help %q is missing backend %q", c.name, help, b)
			}
		}
	}
}

func defaultPromptOptions(t *testing.T) AIPromptOptions {
	t.Helper()
	return AIPromptOptions{
		AIRuntimeOptions: AIRuntimeOptions{
			Temperature: "0",
		},
		Timeout: "120s",
		Prompt:  "hello",
	}
}
