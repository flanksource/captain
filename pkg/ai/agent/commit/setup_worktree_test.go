package commit

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

// TestSetupWorktreeCommitsShellEdits drives isolation through the real setup
// plugin rather than a faked Branch: a worktree created by spec.setup.checkout
// must read as isolated, so a stage-less policy commits a file the agent wrote
// through the shell — one no edit tool recorded — instead of refusing the run.
func TestSetupWorktreeCommitsShellEdits(t *testing.T) {
	repo, err := filepath.Abs(newRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	plugin := &setup.Plugin{BaseDir: t.TempDir()}
	hc := &agent.HookContext{
		Context: context.Background(),
		Request: &ai.Request{Prompt: api.Prompt{User: testPrompt}, Setup: &shell.Setup{Checkout: &shell.Checkout{
			Mode: shell.CheckoutLocal, Path: repo, Worktree: &shell.Worktree{Mode: shell.WorktreeNew},
		}}},
		Response: &ai.Response{Workspace: &api.Workspace{}},
		Hooks:    []any{plugin},
	}
	if err := plugin.PreRun(hc); err != nil {
		t.Fatalf("setup PreRun: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Post(hc, agent.PhaseRun) })

	var stage api.CommitStage
	h := New(api.Commit{On: api.CommitOnRun, Message: "feat: via shell"})
	h.Do = func(_ *agent.HookContext, plan Plan) (string, error) {
		stage = plan.Stage
		return h.run(plan)
	}
	wt := hc.Workspace().Cwd
	write(t, wt, "via-shell.go", "package main\n")

	if err := h.Post(hc, agent.PhaseRun); err != nil {
		t.Fatalf("run phase: %v", err)
	}
	if stage != api.CommitStageWorktree {
		t.Errorf("stage = %q, want %q for a setup-created worktree", stage, api.CommitStageWorktree)
	}
	if got := filesInHead(t, wt, "HEAD"); fmt.Sprint(got) != fmt.Sprint([]string{"via-shell.go"}) {
		t.Errorf("commit touched %v, want the shell-written file", got)
	}
}
