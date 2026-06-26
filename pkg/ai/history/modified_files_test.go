package history

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestModifiedFiles(t *testing.T) {
	toolUses := []ToolUse{
		{Tool: "Read", Input: map[string]any{"file_path": "read-only.go"}},
		{Tool: "Edit", Input: map[string]any{"file_path": "a.go"}},
		{Tool: "Bash", Input: map[string]any{"command": "go build ./..."}},
		{Tool: "Write", Input: map[string]any{"file_path": "b.go"}},
		{Tool: "MultiEdit", Input: map[string]any{"file_path": "c.go"}},
		{Tool: "Edit", Input: map[string]any{"file_path": "a.go"}}, // dup
		{Tool: "NotebookEdit", Input: map[string]any{"notebook_path": "nb.ipynb"}},
		{Tool: "Grep", Input: map[string]any{"pattern": "foo"}},
		{Tool: "Write", Input: map[string]any{"file_path": ""}}, // empty
	}

	got := ModifiedFiles(toolUses)
	want := []string{"a.go", "b.go", "c.go", "nb.ipynb"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModifiedFiles = %v, want %v", got, want)
	}
}

func TestSessionModifiedFiles(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "session.jsonl")
	lines := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"x.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"new.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}
`
	if err := os.WriteFile(session, []byte(lines), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	got, err := SessionModifiedFiles(session)
	if err != nil {
		t.Fatalf("SessionModifiedFiles: %v", err)
	}
	want := []string{"main.go", "new.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SessionModifiedFiles = %v, want %v", got, want)
	}
}

func TestFindSessionFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projects := filepath.Join(home, ".claude", "projects", "-repo-x")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	want := filepath.Join(projects, "sess-123.jsonl")
	if err := os.WriteFile(want, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	got, err := FindSessionFile("sess-123")
	if err != nil {
		t.Fatalf("FindSessionFile: %v", err)
	}
	if got != want {
		t.Fatalf("FindSessionFile = %q, want %q", got, want)
	}

	if _, err := FindSessionFile("missing"); err == nil {
		t.Fatalf("FindSessionFile(missing) = nil error, want not-found error")
	}
}
