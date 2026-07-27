package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
	wantVariables := []PromptVariable{
		{Name: "maxBodyLines", Type: "integer", Description: "Maximum commit-message body lines; zero omits the cap", Required: true},
		{Name: "patch", Type: "string", Description: "Git patch to summarize", Required: true},
	}
	if !reflect.DeepEqual(commit.Variables, wantVariables) {
		t.Fatalf("embedded prompt variables = %+v, want %+v", commit.Variables, wantVariables)
	}
	detail, err := getPrompt(context.Background(), commit.ID)
	if err != nil {
		t.Fatalf("getPrompt(commit) err = %v", err)
	}
	wantInputSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"patch", "maxBodyLines"},
		"properties": map[string]any{
			"patch": map[string]any{
				"type":        "string",
				"description": "Git patch to summarize",
			},
			"maxBodyLines": map[string]any{
				"type":        "integer",
				"description": "Maximum commit-message body lines; zero omits the cap",
			},
		},
	}
	if !reflect.DeepEqual(detail.InputSchema, wantInputSchema) {
		t.Fatalf("embedded prompt input schema = %#v, want %#v", detail.InputSchema, wantInputSchema)
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
