package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// permissions.directories is the runtime-agnostic grant for paths outside the
// working directory. Before it existed only the interactive cmux path could emit
// --add-dir, so a headless run in a worktree had no way to be told about its own
// parent checkout and stalled on a permission request instead.
//
// Each runtime hand-writes its arguments — there is no shared emitter — so the
// only thing that keeps them agreeing is a test that asks each one.

// addDirValues returns the variadic values that follow flag. The CLIs declare
// `--add-dir <directories...>`, so one flag carries every directory.
func addDirValues(args []string, flag string) []string {
	start := slices.Index(args, flag)
	if start < 0 {
		return nil
	}
	var values []string
	for _, arg := range args[start+1:] {
		if strings.HasPrefix(arg, "-") {
			break
		}
		values = append(values, arg)
	}
	return values
}

func directoriesRequest() ai.Request {
	return ai.Request{
		Prompt: api.Prompt{User: "hi"},
		// The duplicate and the blank are the shapes layering actually produces: a
		// profile naming a path the worktree also contributed, and an empty entry
		// from a template that rendered to nothing.
		Permissions: api.Permissions{
			Directories: []string{"/repo/parent", " ", "/repo/parent", "/repo/sibling"},
		},
	}
}

func TestClaudeCLIEmitsAdditionalDirectories(t *testing.T) {
	args, cleanup, err := buildClaudeCLIArgsWithMCP("claude-opus-5", directoriesRequest(), nil)
	if err != nil {
		t.Fatalf("build claude CLI args: %v", err)
	}
	defer cleanup()

	got := addDirValues(args, "--add-dir")
	want := []string{"/repo/parent", "/repo/sibling"}
	if !slices.Equal(got, want) {
		t.Fatalf("--add-dir = %v, want %v (blanks dropped, duplicates collapsed)", got, want)
	}
}

func TestCodexCLIEmitsAdditionalDirectories(t *testing.T) {
	args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "codex"}, directoriesRequest())
	if err != nil {
		t.Fatalf("build codex CLI args: %v", err)
	}
	defer cleanup()

	got := addDirValues(args, "--add-dir")
	want := []string{"/repo/parent", "/repo/sibling"}
	if !slices.Equal(got, want) {
		t.Fatalf("--add-dir = %v, want %v", got, want)
	}
}

// A run that names no directories must not emit the flag at all: an empty
// --add-dir is a CLI error, not a no-op.
func TestNoDirectoriesEmitsNoAddDir(t *testing.T) {
	req := ai.Request{Prompt: api.Prompt{User: "hi"}}

	claudeArgs, cleanup, err := buildClaudeCLIArgsWithMCP("claude-opus-5", req, nil)
	if err != nil {
		t.Fatalf("build claude CLI args: %v", err)
	}
	defer cleanup()
	if slices.Contains(claudeArgs, "--add-dir") {
		t.Fatalf("claude args carry --add-dir with no directories declared: %s", strings.Join(claudeArgs, " "))
	}

	codexArgs, codexCleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "codex"}, req)
	if err != nil {
		t.Fatalf("build codex CLI args: %v", err)
	}
	defer codexCleanup()
	if slices.Contains(codexArgs, "--add-dir") {
		t.Fatalf("codex args carry --add-dir with no directories declared: %s", strings.Join(codexArgs, " "))
	}
}

// A runtime with no way to widen its tool scope must refuse the grant rather
// than accept it and drop it: silently ignoring it reinstates exactly the stall
// the field exists to prevent, on a runtime the caller believes is configured.
func TestUnsupportedRuntimesRefuseDirectories(t *testing.T) {
	if _, err := buildGeminiCLIArgs("gemini-2.5-pro", directoriesRequest()); err == nil {
		t.Fatal("gemini-cli accepted permissions.directories it cannot honour")
	}
	if _, err := buildTurnStartParams("codex", directoriesRequest(), "thread-1", nil); err == nil {
		t.Fatal("codex app-server accepted permissions.directories it cannot honour")
	}

	// ...and a request naming none still builds.
	plain := ai.Request{Prompt: api.Prompt{User: "hi"}}
	if _, err := buildGeminiCLIArgs("gemini-2.5-pro", plain); err != nil {
		t.Fatalf("gemini-cli rejected a request with no directories: %v", err)
	}
	if _, err := buildTurnStartParams("codex", plain, "thread-1", nil); err != nil {
		t.Fatalf("codex app-server rejected a request with no directories: %v", err)
	}
}
