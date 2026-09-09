package setup_test

import (
	"context"
	"slices"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/commons-db/shell"
)

// `git worktree add` brings no gitignored content, so a run in a worktree
// reaches its parent checkout for installed dependencies and build output. With
// the worktree as its only allowed directory, every such read becomes a
// permission request — and a headless run has nobody to answer one, so it parks
// until the approval expires.
//
// The grant is made where the worktree is created because that is the only place
// both paths are in hand: Setup.Cwd still names the origin on the way in, and
// Prepare replaces it with the worktree on the way out.
func TestApply_WorktreeContributesItsParentCheckout(t *testing.T) {
	repo := gitRepo(t)
	req := &ai.Request{Setup: &shell.Setup{
		Cwd: repo,
		Checkout: &shell.Checkout{
			Mode:     shell.CheckoutLocal,
			Path:     repo,
			Worktree: &shell.Worktree{Mode: shell.WorktreeNew},
		},
	}}

	res, err := setup.Apply(context.Background(), req, repo)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() {
		if res != nil && res.Cleanup != nil {
			_ = res.Cleanup()
		}
	})

	if res.Cwd == repo {
		t.Fatalf("Cwd = %q, want a worktree distinct from the origin", res.Cwd)
	}
	if !slices.Contains(req.Permissions.Directories, repo) {
		t.Fatalf("Permissions.Directories = %v, want it to carry the parent checkout %q", req.Permissions.Directories, repo)
	}
}

// A run that was never relocated has nothing to contribute: granting its own cwd
// as an "additional" directory would be noise, and granting anything else would
// be an escalation nobody asked for.
func TestApply_NoWorktreeGrantsNothing(t *testing.T) {
	repo := gitRepo(t)
	req := &ai.Request{Setup: &shell.Setup{Cwd: repo}}

	res, err := setup.Apply(context.Background(), req, repo)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() {
		if res != nil && res.Cleanup != nil {
			_ = res.Cleanup()
		}
	})

	if len(req.Permissions.Directories) != 0 {
		t.Fatalf("Permissions.Directories = %v, want none for a run that stayed where it was", req.Permissions.Directories)
	}
}
