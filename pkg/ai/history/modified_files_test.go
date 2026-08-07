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

// TestModifiedFilesFromPatches: codex expresses every edit as a patch, and
// normalization only rewrites the single-file add/update shapes into Write/Edit
// rows. Multi-file patches, deletes and renames keep the ApplyPatch name, and
// dropping them leaves a commit unable to attribute work the agent plainly did
// — a deleted or renamed file is dirty in git exactly like an edited one.
func TestModifiedFilesFromPatches(t *testing.T) {
	multi := "*** Begin Patch\n" +
		"*** Update File: pkg/a.go\n@@\n-old\n+new\n" +
		"*** Add File: pkg/b.go\n+package b\n" +
		"*** End Patch\n"
	deletion := "*** Begin Patch\n*** Delete File: pkg/gone.go\n*** End Patch\n"
	rename := "*** Begin Patch\n*** Update File: pkg/from.go\n*** Move to: pkg/to.go\n@@\n-old\n+new\n*** End Patch\n"

	cases := []struct {
		name string
		use  ToolUse
		want []string
	}{
		{
			name: "normalized multi-file patch",
			use:  ToolUse{Tool: "ApplyPatch", Input: map[string]any{"input": multi}},
			want: []string{"pkg/a.go", "pkg/b.go"},
		},
		{
			name: "raw codex tool name",
			use:  ToolUse{Tool: "apply_patch", Input: map[string]any{"input": multi}},
			want: []string{"pkg/a.go", "pkg/b.go"},
		},
		{
			name: "deletion",
			use:  ToolUse{Tool: "ApplyPatch", Input: map[string]any{"input": deletion}},
			want: []string{"pkg/gone.go"},
		},
		{
			name: "rename reports both ends",
			use:  ToolUse{Tool: "ApplyPatch", Input: map[string]any{"input": rename}},
			want: []string{"pkg/from.go", "pkg/to.go"},
		},
		{
			name: "patch piped through the shell",
			use:  ToolUse{Tool: "Bash", Input: map[string]any{"command": "apply_patch <<'EOF'\n" + deletion + "EOF"}},
			want: []string{"pkg/gone.go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModifiedFiles([]ToolUse{tc.use}); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ModifiedFiles = %v, want %v", got, tc.want)
			}
		})
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
