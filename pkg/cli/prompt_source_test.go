package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestActionFlagsToOptions_DecodesSlicesBoolsInts(t *testing.T) {
	f := map[string]string{
		"model":           "gpt-4o",
		"backend":         "openai",
		"no-stream":       "true",
		"edit":            "true",
		"allowed-tools":   "Read,Write",
		"skill-dir":       "/a,/b",
		"max-tokens":      "1234",
		"var":             "a=1,b=2",
		"prompt":          "hi",
		"permission-mode": "plan",
	}
	o, err := actionFlagsToOptions(f)
	if err != nil {
		t.Fatalf("actionFlagsToOptions: %v", err)
	}
	if o.Model != "gpt-4o" || o.Backend != "openai" || o.Prompt != "hi" || o.PermissionMode != "plan" {
		t.Fatalf("scalars = %+v", o)
	}
	if !o.NoStream || !o.Edit {
		t.Errorf("bools not decoded: NoStream=%v Edit=%v", o.NoStream, o.Edit)
	}
	if o.MaxTokens != 1234 {
		t.Errorf("max-tokens = %d, want 1234", o.MaxTokens)
	}
	if len(o.AllowedTools) != 2 || o.AllowedTools[0] != "Read" || o.AllowedTools[1] != "Write" {
		t.Errorf("allowed-tools = %v", o.AllowedTools)
	}
	if len(o.SkillDirs) != 2 || o.SkillDirs[0] != "/a" {
		t.Errorf("skill-dir = %v", o.SkillDirs)
	}
	if len(o.Var) != 2 || o.Var[0] != "a=1" {
		t.Errorf("var = %v", o.Var)
	}
	if _, err := actionFlagsToOptions(map[string]string{"max-tokens": "nope"}); err == nil {
		t.Error("expected error on non-numeric max-tokens")
	}
}

func TestLooksLikePromptPath(t *testing.T) {
	paths := []string{"foo.prompt", "./foo.prompt", "/abs/foo.prompt", "dir/foo", ".hidden"}
	for _, p := range paths {
		if !looksLikePromptPath(p) {
			t.Errorf("%q should look like a path", p)
		}
	}
	ids := []string{"Zm9vAGJhcgBiYXoucHJvbXB0", "abc123"} // base64-raw-url ids
	for _, id := range ids {
		if looksLikePromptPath(id) {
			t.Errorf("%q should look like an id, not a path", id)
		}
	}
}

func TestLoadPromptContent_Sources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "greet.prompt")
	body := "---\nmodel: claude-opus-4\n---\n{{role \"user\"}}\nHello\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// filepath positional
	content, _, usedStdin, _, err := loadPromptContent(ctx, path, AIPromptOptions{}, "")
	if err != nil || content != body || usedStdin {
		t.Fatalf("file source: content=%q usedStdin=%v err=%v", content, usedStdin, err)
	}
	// --prompt/-p text (positional empty)
	content, _, _, _, err = loadPromptContent(ctx, "", AIPromptOptions{AIRuntimeOptions: AIRuntimeOptions{}, Prompt: "inline body"}, "")
	if err != nil || content != "inline body" {
		t.Fatalf("prompt source: content=%q err=%v", content, err)
	}
	// stdin
	content, _, usedStdin, _, err = loadPromptContent(ctx, "", AIPromptOptions{}, "piped body")
	if err != nil || content != "piped body" || !usedStdin {
		t.Fatalf("stdin source: content=%q usedStdin=%v err=%v", content, usedStdin, err)
	}
	// nothing
	if _, _, _, _, err = loadPromptContent(ctx, "", AIPromptOptions{}, ""); err == nil {
		t.Error("expected error when no source is given")
	}
}
