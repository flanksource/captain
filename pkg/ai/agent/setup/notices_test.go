package setup_test

import (
	"os"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/commons-db/shell"
)

func noticeTexts(hc *agent.HookContext) []string {
	var texts []string
	for _, n := range hc.Workspace().Notices {
		texts = append(texts, n.Text)
	}
	return texts
}

func worktreeRun(t *testing.T, keep bool) (*setup.Plugin, *agent.HookContext) {
	t.Helper()
	repo := gitRepo(t)
	req := &ai.Request{Setup: &shell.Setup{Checkout: &shell.Checkout{
		Mode: shell.CheckoutLocal, Path: repo,
		Worktree: &shell.Worktree{Mode: shell.WorktreeNew, Keep: keep},
	}}}
	plugin := &setup.Plugin{BaseDir: t.TempDir()}
	return plugin, newHookContext(t, req, "", plugin)
}

// A relocated run works somewhere the caller never named, so where it went and
// what became of it are the two things the run's stream must say.
func TestPlugin_ReportsTheWorktreeItCreatesAndRemoves(t *testing.T) {
	plugin, hc := worktreeRun(t, false)

	if err := plugin.PreRun(hc); err != nil {
		t.Fatalf("PreRun: %v", err)
	}
	path, wt := hc.Workspace().Cwd, hc.Workspace().Worktree
	if err := plugin.Post(hc, agent.PhaseRun); err != nil {
		t.Fatalf("Post: %v", err)
	}

	want := []string{
		"[pre-run] worktree " + path + " on " + wt.Branch + " from " + wt.Base[:7],
		"[post-run] removed worktree " + path + "; deleted branch " + wt.Branch,
	}
	if got := noticeTexts(hc); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("notices = %q, want %q", got, want)
	}
}

func TestPlugin_ReportsAKeptWorktree(t *testing.T) {
	plugin, hc := worktreeRun(t, true)

	if err := plugin.PreRun(hc); err != nil {
		t.Fatalf("PreRun: %v", err)
	}
	path, branch := hc.Workspace().Cwd, hc.Workspace().Worktree.Branch
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	if err := plugin.Post(hc, agent.PhaseRun); err != nil {
		t.Fatalf("Post: %v", err)
	}

	got := noticeTexts(hc)
	if len(got) != 2 || got[1] != "[post-run] kept worktree "+path+" on "+branch+": keep requested" {
		t.Errorf("notices = %q, want the kept worktree %q reported at teardown", got, path)
	}
}

// A setup that relocates nothing (dotenv, env vars only) leaves the run where it
// was; announcing it would be noise.
func TestPlugin_StaysQuietWhenNothingRelocates(t *testing.T) {
	req := &ai.Request{Setup: &shell.Setup{Cwd: gitRepo(t)}}
	plugin := &setup.Plugin{}
	hc := newHookContext(t, req, "", plugin)

	if err := plugin.PreRun(hc); err != nil {
		t.Fatalf("PreRun: %v", err)
	}
	if err := plugin.Post(hc, agent.PhaseRun); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if got := noticeTexts(hc); len(got) != 0 {
		t.Errorf("notices = %q, want none", got)
	}
}
