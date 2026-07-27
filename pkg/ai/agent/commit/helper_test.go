package commit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"
)

const testPrompt = "add a greeting helper"

// newRepo creates a real git repo with one seed commit under the project's
// .tmp/, and returns its path. Real repos rather than fakes: every behaviour
// worth testing here (fixup chains, autosquash, staged deletions, rename
// records) is git's, and a stub of git would only assert our own assumptions.
func newRepo(t *testing.T) string {
	t.Helper()
	base := filepath.Join("..", "..", "..", "..", ".tmp")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("create .tmp: %v", err)
	}
	dir, err := os.MkdirTemp(base, "commit-test-")
	if err != nil {
		t.Fatalf("create test repo dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// --template= skips git's sample-hook copy, which both slows the test down
	// and needs permissions a sandbox may not grant.
	mustGit(t, dir, "init", "-b", "main", "--template=")
	mustGit(t, dir, "config", "user.email", "captain-test@example.com")
	mustGit(t, dir, "config", "user.name", "Captain Test")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	// Ignore the developer's global excludes file: it commonly hides .env and
	// dist/, which are exactly the paths the gate tests need git to report.
	mustGit(t, dir, "config", "core.excludesFile", "/dev/null")
	write(t, dir, "README.md", "seed\n")
	mustGit(t, dir, "add", "README.md")
	mustGit(t, dir, "commit", "-m", "chore: seed")
	return dir
}

// newRepoWithoutCommits creates an initialized but empty repo, so the run's
// first commit is a root commit with no parent.
func newRepoWithoutCommits(t *testing.T) string {
	t.Helper()
	base := filepath.Join("..", "..", "..", "..", ".tmp")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("create .tmp: %v", err)
	}
	dir, err := os.MkdirTemp(base, "commit-root-")
	if err != nil {
		t.Fatalf("create test repo dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// --template= skips git's sample-hook copy, which both slows the test down
	// and needs permissions a sandbox may not grant.
	mustGit(t, dir, "init", "-b", "main", "--template=")
	mustGit(t, dir, "config", "user.email", "captain-test@example.com")
	mustGit(t, dir, "config", "user.name", "Captain Test")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	// Ignore the developer's global excludes file: it commonly hides .env and
	// dist/, which are exactly the paths the gate tests need git to report.
	mustGit(t, dir, "config", "core.excludesFile", "/dev/null")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func mustRemove(t *testing.T, dir, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, rel)); err != nil {
		t.Fatalf("remove %s: %v", rel, err)
	}
}

// isolated builds the hook context for a run in its own worktree branch — the
// shape produced by worktree.Plugin.PreRun, where the whole tree is the agent's.
func isolated(dir string) *agent.HookContext {
	return newContext(dir, "agent/test")
}

// shared builds the hook context for a run in the caller's own checkout, where
// the tree may hold uncommitted work that is not the agent's.
func shared(dir string) *agent.HookContext {
	return newContext(dir, "")
}

func newContext(dir, branch string) *agent.HookContext {
	return &agent.HookContext{
		Context:  context.Background(),
		Request:  &ai.Request{Prompt: api.Prompt{User: testPrompt}},
		Response: &ai.Response{Workspace: &api.Workspace{Repo: dir, Cwd: dir, Branch: branch}},
	}
}

// changed records paths as agent-modified, the way Runner.recordEvent does from
// the agent's Edit/Write tool calls.
func changed(hc *agent.HookContext, paths ...string) {
	hc.Workspace().Changed = append(hc.Workspace().Changed, paths...)
}

// subjects lists commit subjects newest-first.
func subjects(t *testing.T, dir string) []string {
	t.Helper()
	out := mustGit(t, dir, "log", "--format=%s")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// commitCount counts commits reachable from HEAD.
func commitCount(t *testing.T, dir string) int {
	t.Helper()
	return len(subjects(t, dir))
}

// filesInHead lists the paths a commit touched.
func filesInHead(t *testing.T, dir, ref string) []string {
	t.Helper()
	out := mustGit(t, dir, "show", "--name-only", "--format=", ref)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// isClean reports whether the tree has no uncommitted changes at all.
func isClean(t *testing.T, dir string) bool {
	t.Helper()
	return mustGit(t, dir, "status", "--porcelain") == ""
}

// rebaseInProgress reports whether git was left mid-rebase — the state an
// aborted autosquash must never leave behind.
func rebaseInProgress(t *testing.T, dir string) bool {
	t.Helper()
	gitDir := mustGit(t, dir, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	for _, marker := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			return true
		}
	}
	return false
}

// gitAvailable reports whether a usable git binary is on PATH; the suite is
// meaningless without one and says so rather than failing obscurely.
func gitAvailable() bool {
	return exec.NewExec("git", "--version").Run().Result().ExitCode == 0
}

func TestMain(m *testing.M) {
	if !gitAvailable() {
		os.Stderr.WriteString("commit: git is required for this package's tests\n")
		os.Exit(1)
	}
	os.Exit(m.Run())
}
