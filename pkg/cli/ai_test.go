package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
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

	if req.Prompt.User != "hello" {
		t.Errorf("Prompt = %q, want hello", req.Prompt.User)
	}
	if req.Budget.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096 built-in default", req.Budget.MaxTokens)
	}
	for name, got := range map[string]bool{
		"NoMCP": req.Permissions.MCP.Disabled, "NoHooks": req.Memory.SkipHooks, "NoSkills": req.Memory.SkipSkills,
		"NoUser": req.Memory.SkipUser, "NoProject": req.Memory.SkipProject, "NoMemory": req.Memory.SkipMemory,
		"Bare": req.Memory.Bare, "Edit": req.Permissions.HasPreset(api.PresetEdit),
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
		get    func(ai.Request) bool
	}{
		{"no-mcp", func(o *AIPromptOptions) { o.NoMCP = true }, func(r ai.Request) bool { return r.Permissions.MCP.Disabled }},
		{"no-hooks", func(o *AIPromptOptions) { o.NoHooks = true }, func(r ai.Request) bool { return r.Memory.SkipHooks }},
		{"no-skills", func(o *AIPromptOptions) { o.NoSkills = true }, func(r ai.Request) bool { return r.Memory.SkipSkills }},
		{"no-user", func(o *AIPromptOptions) { o.NoUser = true }, func(r ai.Request) bool { return r.Memory.SkipUser }},
		{"no-project", func(o *AIPromptOptions) { o.NoProject = true }, func(r ai.Request) bool { return r.Memory.SkipProject }},
		{"no-memory", func(o *AIPromptOptions) { o.NoMemory = true }, func(r ai.Request) bool { return r.Memory.SkipMemory }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaultPromptOptions(t)
			tc.mutate(&opts)
			req, err := opts.ToRequest()
			if err != nil {
				t.Fatalf("ToRequest: %v", err)
			}
			if !tc.get(req) {
				t.Errorf("flag --%s did not set the mapped request field", tc.name)
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
	if req.Prompt.System != "be careful" {
		t.Errorf("SystemPrompt = %q", req.Prompt.System)
	}
	if req.Prompt.AppendSystem != "also be brief" {
		t.Errorf("AppendSystemPrompt = %q", req.Prompt.AppendSystem)
	}
	if req.Permissions.Mode != api.PermissionAcceptEdits {
		t.Errorf("PermissionMode = %q", req.Permissions.Mode)
	}
	if !req.Permissions.HasPreset(api.PresetEdit) {
		t.Error("Edit preset not propagated")
	}
	if !reflect.DeepEqual(req.Permissions.Tools.Allow, []string{"Read", "Bash"}) {
		t.Errorf("AllowedTools = %v", req.Permissions.Tools.Allow)
	}
	if !reflect.DeepEqual(req.Permissions.Tools.Deny, []string{"Write"}) {
		t.Errorf("DisallowedTools = %v", req.Permissions.Tools.Deny)
	}
	if !reflect.DeepEqual(req.Memory.Skills, []string{"/skills/a", "/skills/b"}) {
		t.Errorf("SkillDirs = %v", req.Memory.Skills)
	}
	if !req.Memory.Bare {
		t.Error("Bare not propagated")
	}
	if req.Budget.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d", req.Budget.MaxTokens)
	}
	if temp, ok := req.Temp(); !ok || temp != 0.5 {
		t.Errorf("Temperature = %v (set=%v)", temp, ok)
	}
	if req.Effort != api.EffortHigh {
		t.Errorf("ReasoningEffort = %q", req.Effort)
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
		if req.Budget.MaxTokens != 2048 {
			t.Errorf("MaxTokens = %d, want 2048 (explicit flag)", req.Budget.MaxTokens)
		}
	})
	t.Run("unset falls back to saved", func(t *testing.T) {
		seedSavedAI(t, "ai:\n  maxTokens: 16000\n")
		req, err := AIRuntimeOptions{}.ToRequest("", "", "p")
		if err != nil {
			t.Fatal(err)
		}
		if req.Budget.MaxTokens != 16000 {
			t.Errorf("MaxTokens = %d, want 16000 (saved)", req.Budget.MaxTokens)
		}
	})
	t.Run("unset with no saved uses built-in 4096", func(t *testing.T) {
		isolateSavedAI(t)
		req, err := AIRuntimeOptions{}.ToRequest("", "", "p")
		if err != nil {
			t.Fatal(err)
		}
		if req.Budget.MaxTokens != 4096 {
			t.Errorf("MaxTokens = %d, want 4096 (built-in)", req.Budget.MaxTokens)
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

	if req.Prompt.System != "sys" || req.Prompt.User != "user" {
		t.Errorf("prompt fields = %q/%q, want sys/user", req.Prompt.System, req.Prompt.User)
	}
	if req.Budget.MaxTokens != 16000 {
		t.Errorf("MaxTokens = %d, want 16000 (saved overlay)", req.Budget.MaxTokens)
	}
	if req.Effort != api.EffortHigh {
		t.Errorf("ReasoningEffort = %q, want high (flag overrides saved)", req.Effort)
	}
	for name, got := range map[string]bool{
		"NoMCP": req.Permissions.MCP.Disabled, "NoHooks": req.Memory.SkipHooks, "NoSkills": req.Memory.SkipSkills,
		"NoUser": req.Memory.SkipUser, "NoProject": req.Memory.SkipProject, "NoMemory": req.Memory.SkipMemory,
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
