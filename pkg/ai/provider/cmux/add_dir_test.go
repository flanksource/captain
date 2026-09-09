package cmux

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

// --add-dir has two sources on the cmux path: the Spec-level
// permissions.directories every runtime now honours, and the free-form cmux
// args that predate it. Both are real, so they merge into one flag list rather
// than each appending — a path a profile names and a worktree also contributes
// must be granted once.
func TestAgentCommandMergesAddDirSources(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
		extra any
	}{
		{name: "claude", agent: "claude", extra: &api.ClaudeCmuxOptions{AddDir: []string{"/repo/parent", "/from/cli-args"}}},
		{name: "codex", agent: "codex", extra: &api.CodexCmuxOptions{AddDir: []string{"/repo/parent", "/from/cli-args"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := AgentCommand(AgentCommandOpts{
				Agent:       tc.agent,
				Directories: []string{"/repo/parent", "/repo/sibling"},
				Extra:       tc.extra,
			})
			if got := strings.Count(command, "/repo/parent"); got != 1 {
				t.Fatalf("a directory named by both sources appears %d times, want 1: %s", got, command)
			}
			if want := "--add-dir /repo/parent /repo/sibling /from/cli-args"; !strings.Contains(command, want) {
				t.Fatalf("command missing %q: %s", want, command)
			}
		})
	}
}

// The Spec-level grant has to reach the command line even when no free-form cmux
// args were supplied, which is the common shape for a run captain assembled.
func TestAgentCommandEmitsSpecDirectoriesWithoutExtra(t *testing.T) {
	command := AgentCommand(AgentCommandOpts{Agent: "claude", Directories: []string{"/repo/parent"}})
	if !strings.Contains(command, "--add-dir /repo/parent") {
		t.Fatalf("spec directories dropped when no cmux args are set: %s", command)
	}
}

func TestAgentCommandOmitsAddDirWhenNoneDeclared(t *testing.T) {
	command := AgentCommand(AgentCommandOpts{Agent: "claude"})
	if strings.Contains(command, "--add-dir") {
		t.Fatalf("command carries --add-dir with no directories declared: %s", command)
	}
}
