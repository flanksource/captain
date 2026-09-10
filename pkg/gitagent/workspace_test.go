package gitagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskRuntimeIdentityUsesModelAndEffort(t *testing.T) {
	got := taskRuntimeIdentity([]byte(`{"model":"gpt-5.6-sol","provider":"openai","mode":"agent","effort":"high"}`))
	if got != "agent:gpt-5.6-sol:high" {
		t.Fatalf("identity = %q", got)
	}
	// A payload naming no mode names no runtime, so the log identity falls back to
	// the generic agent name rather than half-labelling the run.
	if got := taskRuntimeIdentity([]byte(`{}`)); got != "captain-agent" {
		t.Fatalf("modeless identity = %q", got)
	}
	if got := taskRuntimeIdentity([]byte(`{"model":"gpt-5.6-sol"}`)); got != "captain-agent" {
		t.Fatalf("modeless identity = %q", got)
	}
}

func TestSetupAgentWorkspacePinsRuntimeIdentity(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "sidecar.git")
	if err := InitSidecar(ctx, repo); err != nil {
		t.Fatal(err)
	}
	commit, err := BuildControlCommit(ctx, repo, nil, map[string][]byte{"seed.txt": []byte("seed\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveTaskState(repo, &TaskState{Task: "t-identity"}); err != nil {
		t.Fatal(err)
	}
	workdir, err := SetupAgentWorkspace(ctx, repo, "t-identity", commit, "agent:gpt-5.6-sol:high")
	if err != nil {
		t.Fatal(err)
	}
	env := ScrubGitEnv(os.Environ())
	if got, err := runGit(ctx, workdir, env, "config", "user.name"); err != nil || got != "agent:gpt-5.6-sol:high" {
		t.Fatalf("user.name = %q, %v", got, err)
	}
	if got, err := runGit(ctx, workdir, env, "config", "user.email"); err != nil || got != "agent@captain.local" {
		t.Fatalf("user.email = %q, %v", got, err)
	}
}

// The workspace path is published only once it is a workspace. `git clone`
// creates its target first and writes the branch ref and upstream config last,
// so cloning straight onto the final path let an observer read a directory
// whose HEAD named a branch that did not exist yet — which is how the git-agent
// e2e cycle intermittently failed its `@{u}` check on CI.
func TestSetupAgentWorkspaceIsCompleteWhenItAppears(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "sidecar.git")
	if err := InitSidecar(ctx, repo); err != nil {
		t.Fatal(err)
	}
	commit, err := BuildControlCommit(ctx, repo, nil, map[string][]byte{"seed.txt": []byte("seed\n")})
	if err != nil {
		t.Fatal(err)
	}
	const task = "t-complete"
	if err := SaveTaskState(repo, &TaskState{Task: task}); err != nil {
		t.Fatal(err)
	}
	workdir, err := SetupAgentWorkspace(ctx, repo, task, commit, "captain-agent")
	if err != nil {
		t.Fatal(err)
	}
	// A bare `git push` needs an upstream, and it has to be there the moment the
	// path exists — not a few milliseconds later (H17).
	env := ScrubGitEnv(os.Environ())
	upstream, err := runGit(ctx, workdir, env, "rev-parse", "--abbrev-ref", "@{u}")
	if err != nil {
		t.Fatalf("the published workspace has no upstream: %v", err)
	}
	if !strings.HasSuffix(upstream, task) {
		t.Fatalf("upstream = %q, want one ending in %q", upstream, task)
	}
	// The staging directory the clone was built in is gone, so a later dispatch
	// cannot mistake a half-built workspace for a finished one.
	entries, err := os.ReadDir(taskStateDir(repo, task))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".worktree-") {
			t.Fatalf("staging directory %q survived the setup", entry.Name())
		}
	}
}

// A dispatch that launches nothing leaves the supervisor waiting out its whole
// budget on work that never started — a silence indistinguishable from an
// agent still thinking. Empty must therefore be an error, and "no agent" must
// be spelled explicitly.
func TestLaunchAgentRefusesAnEmptyCommand(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(taskStateDir(repo, "t-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"", "   "} {
		err := LaunchAgent(repo, "t-1", filepath.Join(repo, "wt"), "task.json", command)
		if err == nil || !strings.Contains(err.Error(), "no agent command") {
			t.Fatalf("command %q: err = %v, want a refusal", command, err)
		}
	}
}

func TestLaunchAgentRecordsAnExplicitOptOut(t *testing.T) {
	repo := t.TempDir()
	dir := taskStateDir(repo, "t-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(repo, "wt")
	if err := LaunchAgent(repo, "t-1", workdir, "task.json", NoAgentCommand); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(dir, "agent.stdout.log"))
	if err != nil {
		t.Fatalf("an opt-out must leave a diagnosable record: %v", err)
	}
	if !strings.Contains(string(log), workdir) {
		t.Fatalf("the record must name the worktree to push from: %s", log)
	}
}

// A backend that configures no agentCommand still gets a real agent, supplied
// by the host. Falling through to empty is what made the advertised flow wait
// forever.
func TestAgentCommandForFallsBackToTheHostDefault(t *testing.T) {
	host := HookHost{DefaultAgentCommand: func(repo, task string) string {
		return "captain run-task " + repo + " " + task
	}}
	if got := host.agentCommandFor("/repo.git", "t-1"); got != "captain run-task /repo.git t-1" {
		t.Fatalf("agentCommandFor = %q", got)
	}

	host.Runtime.AgentCommand = "my-own-agent"
	if got := host.agentCommandFor("/repo.git", "t-1"); got != "my-own-agent" {
		t.Fatalf("a configured command must win, got %q", got)
	}

	bare := HookHost{}
	if got := bare.agentCommandFor("/repo.git", "t-1"); got != "" {
		t.Fatalf("with no default and no config the result is empty (LaunchAgent then refuses), got %q", got)
	}
}
