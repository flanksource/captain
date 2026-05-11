package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/captainconfig"
)

// isolateSavedAI redirects captainconfig.Path() to an empty file inside
// t.TempDir() so loadSavedAI() returns zero defaults rather than leaking
// the developer's real ~/.captain.yaml into table-test expectations.
func isolateSavedAI(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".captain.yaml")
	if err := os.WriteFile(p, []byte(""), 0o644); err != nil {
		t.Fatalf("seed empty captain config: %v", err)
	}
	captainconfig.SetPathForTesting(p)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
}

func TestAIPromptOptions_ToRequest_Defaults(t *testing.T) {
	isolateSavedAI(t)
	opts := defaultPromptOptions(t)
	req := opts.ToRequest()

	if req.Prompt != "hello" {
		t.Errorf("Prompt = %q, want hello", req.Prompt)
	}
	for name, got := range map[string]bool{
		"NoMCP":     req.NoMCP,
		"NoHooks":   req.NoHooks,
		"NoSkills":  req.NoSkills,
		"NoUser":    req.NoUser,
		"NoProject": req.NoProject,
		"NoMemory":  req.NoMemory,
		"Bare":      req.Bare,
		"Edit":      req.Edit,
	} {
		if got {
			t.Errorf("%s = true; default-shape options must not flip any No*/Bare/Edit", name)
		}
	}
}

func TestAIPromptOptions_ToRequest_TruthyInversion(t *testing.T) {
	isolateSavedAI(t)
	cases := []struct {
		name   string
		mutate func(*AIPromptOptions)
		check  func(t *testing.T, got any)
	}{
		{
			name:   "mcp=false",
			mutate: func(o *AIPromptOptions) { o.MCP = false },
			check:  func(t *testing.T, r any) { mustBool(t, r.(bool), true, "NoMCP") },
		},
		{
			name:   "hooks=false",
			mutate: func(o *AIPromptOptions) { o.Hooks = false },
			check:  func(t *testing.T, r any) { mustBool(t, r.(bool), true, "NoHooks") },
		},
		{
			name:   "skills=false",
			mutate: func(o *AIPromptOptions) { o.Skills = false },
			check:  func(t *testing.T, r any) { mustBool(t, r.(bool), true, "NoSkills") },
		},
		{
			name:   "user=false",
			mutate: func(o *AIPromptOptions) { o.User = false },
			check:  func(t *testing.T, r any) { mustBool(t, r.(bool), true, "NoUser") },
		},
		{
			name:   "project=false",
			mutate: func(o *AIPromptOptions) { o.Project = false },
			check:  func(t *testing.T, r any) { mustBool(t, r.(bool), true, "NoProject") },
		},
		{
			name:   "memory=false",
			mutate: func(o *AIPromptOptions) { o.Memory = false },
			check:  func(t *testing.T, r any) { mustBool(t, r.(bool), true, "NoMemory") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := defaultPromptOptions(t)
			tc.mutate(&opts)
			req := opts.ToRequest()
			// Map field names back to Request fields the test expects.
			rv := reflect.ValueOf(req)
			fieldMap := map[string]string{
				"NoMCP":     "NoMCP",
				"NoHooks":   "NoHooks",
				"NoSkills":  "NoSkills",
				"NoUser":    "NoUser",
				"NoProject": "NoProject",
				"NoMemory":  "NoMemory",
			}
			for _, name := range fieldMap {
				if rv.FieldByName(name).Kind() != reflect.Bool {
					continue
				}
			}
			// Each test case checks one inverted flag.
			lookup := map[string]string{
				"mcp=false":     "NoMCP",
				"hooks=false":   "NoHooks",
				"skills=false":  "NoSkills",
				"user=false":    "NoUser",
				"project=false": "NoProject",
				"memory=false":  "NoMemory",
			}
			tc.check(t, rv.FieldByName(lookup[tc.name]).Bool())
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

	req := opts.ToRequest()
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
}

// TestAIRuntimeOptions_ToRequest_OverlaysSaved verifies the path gavel (and any
// other embedder) takes: AIRuntimeOptions with zero/default flag values should
// still pick up NoMCP/NoHooks/MaxTokens/ReasoningEffort from ~/.captain.yaml.
func TestAIRuntimeOptions_ToRequest_OverlaysSaved(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), ".captain.yaml")
	saved := []byte("ai:\n  noMCP: true\n  noHooks: true\n  noSkills: true\n  noUser: true\n  noProject: true\n  noMemory: true\n  maxTokens: 16000\n  reasoningEffort: high\n")
	if err := os.WriteFile(tmp, saved, 0o644); err != nil {
		t.Fatalf("seed captain config: %v", err)
	}
	captainconfig.SetPathForTesting(tmp)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })

	opts := AIRuntimeOptions{
		MaxTokens: 4096, // sentinel default — should be overridden by saved 16000
		MCP:       true, // flag-default true; saved NoMCP=true must still win
		Hooks:     true,
		Skills:    true,
		User:      true,
		Project:   true,
		Memory:    true,
	}
	req := opts.ToRequest("sys", "", "user")

	if req.SystemPrompt != "sys" {
		t.Errorf("SystemPrompt = %q, want sys", req.SystemPrompt)
	}
	if req.Prompt != "user" {
		t.Errorf("Prompt = %q, want user", req.Prompt)
	}
	if req.MaxTokens != 16000 {
		t.Errorf("MaxTokens = %d, want 16000 (saved overlay)", req.MaxTokens)
	}
	if req.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q, want high", req.ReasoningEffort)
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

func defaultPromptOptions(t *testing.T) AIPromptOptions {
	t.Helper()
	return AIPromptOptions{
		AIRuntimeOptions: AIRuntimeOptions{
			MaxTokens:   4096,
			Temperature: "0",
			MCP:         true,
			Hooks:       true,
			Skills:      true,
			User:        true,
			Project:     true,
			Memory:      true,
		},
		Timeout: "120s",
		Prompt:  "hello",
	}
}

func mustBool(t *testing.T, got, want bool, name string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
