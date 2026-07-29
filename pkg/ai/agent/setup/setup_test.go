package setup_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/ai/agent/worktree"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons/merge"
)

// newHookContext builds the context a Runner would hand a PreRun hook.
func newHookContext(t *testing.T, req *ai.Request, cwd string, hooks ...any) *agent.HookContext {
	t.Helper()
	return &agent.HookContext{
		Context: context.Background(),
		Request: req,
		// Cloned the way Runner.Run clones it, so the test cannot pass by
		// accidentally aliasing the request it is meant to be independent of.
		Original: merge.Clone(*req, api.MergePolicy()),
		Response: &ai.Response{Workspace: &api.Workspace{Cwd: cwd}},
		Hooks:    hooks,
	}
}

// gitRepo creates a repo with one commit, so a local checkout has something to
// resolve. Skips rather than fails when git is unavailable.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"add", "README.md"},
		{"commit", "-q", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

// The defining property of the transform: after setup runs, the spec says where
// the work is, not how to create it. Without this, persisting or resuming a spec
// re-performs the checkout — a second clone into a second directory that nothing
// merges back.
func TestApply_ConsumesTheCheckoutAndIsIdempotent(t *testing.T) {
	repo := gitRepo(t)
	base := t.TempDir()
	req := &ai.Request{Setup: &shell.Setup{
		Checkout: &shell.Checkout{Mode: shell.CheckoutLocal, Path: repo},
	}}

	first, err := setup.Apply(context.Background(), req, base)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() {
		if first != nil && first.Cleanup != nil {
			_ = first.Cleanup()
		}
	})

	if req.Setup.Checkout != nil {
		t.Errorf("Setup.Checkout = %+v, want nil — the checkout was performed, so the request for it is spent", req.Setup.Checkout)
	}
	if req.Cwd() != first.Cwd {
		t.Errorf("Cwd = %q, want the prepared path %q", req.Cwd(), first.Cwd)
	}
	if !filepath.IsAbs(req.Cwd()) {
		t.Errorf("Cwd = %q, want an absolute path so the spec is portable", req.Cwd())
	}

	// Re-applying the result must not check anything out again, and must not
	// re-anchor: the second call deliberately passes a different base dir, which
	// a spec still carrying relative paths or a checkout would follow.
	before := req.Cwd()
	second, err := setup.Apply(context.Background(), req, t.TempDir())
	if err != nil {
		t.Fatalf("Apply (second): %v", err)
	}
	t.Cleanup(func() {
		if second != nil && second.Cleanup != nil {
			_ = second.Cleanup()
		}
	})
	if req.Cwd() != before {
		t.Errorf("Cwd = %q after re-applying, want it unchanged at %q", req.Cwd(), before)
	}
	if req.Setup.Checkout != nil {
		t.Errorf("Setup.Checkout = %+v after re-applying, want nil", req.Setup.Checkout)
	}
}

// Env is an output of shell.Prepare, but callers seed it with run-specific
// variables; losing those silently strips a run's configuration.
func TestApply_PreservesCallerSuppliedEnv(t *testing.T) {
	req := &ai.Request{Setup: &shell.Setup{Cwd: t.TempDir(), Env: []string{"RUN_ID=abc123"}}}

	if _, err := setup.Apply(context.Background(), req, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var found bool
	for _, kv := range req.Setup.Env {
		if kv == "RUN_ID=abc123" {
			found = true
		}
	}
	if !found {
		t.Errorf("Setup.Env = %v, want the caller's RUN_ID preserved", req.Setup.Env)
	}
}

func TestApply_NoSetupIsANoOp(t *testing.T) {
	req := &ai.Request{}
	res, err := setup.Apply(context.Background(), req, "")
	if err != nil || res != nil {
		t.Fatalf("Apply() = %v, %v; want nil, nil for a spec declaring no setup", res, err)
	}
	if req.Setup != nil {
		t.Errorf("Setup = %+v, want a spec with no setup left untouched", req.Setup)
	}
}

// A Post hook needs to know what the run was asked to do — which repo, which
// branch — after PreRun has rewritten the request to say where that landed.
func TestPlugin_PreRun_LeavesOriginalIntact(t *testing.T) {
	repo := gitRepo(t)
	req := &ai.Request{Setup: &shell.Setup{
		Checkout: &shell.Checkout{Mode: shell.CheckoutLocal, Path: repo},
	}}
	plugin := &setup.Plugin{BaseDir: t.TempDir()}
	hc := newHookContext(t, req, "", plugin)

	if err := plugin.PreRun(hc); err != nil {
		t.Fatalf("PreRun: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Post(hc, agent.PhaseRun) })

	if hc.Original.Setup.Checkout == nil {
		t.Fatal("Original.Setup.Checkout = nil, want the pre-transform checkout still readable")
	}
	if got := hc.Original.Setup.Checkout.Path; got != repo {
		t.Errorf("Original checkout path = %q, want %q", got, repo)
	}
	if hc.Request.Setup.Checkout != nil {
		t.Error("Request.Setup.Checkout survived PreRun, want it consumed")
	}
	if hc.Workspace().Cwd != hc.Request.Cwd() {
		t.Errorf("Workspace.Cwd = %q, want it pointed at the prepared %q", hc.Workspace().Cwd, hc.Request.Cwd())
	}
}

// Two hooks that each relocate the run produce two trees and work in one. The
// run then edits a tree nothing merges, which is silent — so it must be an error.
func TestPlugin_PreRun_RejectsASecondIsolator(t *testing.T) {
	req := &ai.Request{Setup: &shell.Setup{
		Checkout: &shell.Checkout{
			Mode:     shell.CheckoutLocal,
			Path:     t.TempDir(),
			Worktree: &shell.Worktree{Mode: shell.WorktreeNew},
		},
	}}
	plugin := &setup.Plugin{}
	wt := &worktree.Plugin{Repo: t.TempDir(), Branch: "feat/x"}
	hc := newHookContext(t, req, "", plugin, wt)

	err := plugin.PreRun(hc)
	if err == nil {
		t.Fatal("PreRun() = nil, want an error naming both isolating hooks")
	}
	for _, want := range []string{"setup", "worktree"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if req.Setup.Checkout == nil {
		t.Error("the rejected run's checkout was consumed anyway, want the spec untouched")
	}
}

// A setup that only supplies env does not relocate anything, so pairing it with
// the worktree hook is the normal composition and must not error.
func TestPlugin_PreRun_EnvOnlySetupIsNotAnIsolator(t *testing.T) {
	req := &ai.Request{Setup: &shell.Setup{Cwd: t.TempDir()}}
	plugin := &setup.Plugin{}
	hc := newHookContext(t, req, "", plugin, &worktree.Plugin{Branch: "feat/x"})

	if err := plugin.PreRun(hc); err != nil {
		t.Fatalf("PreRun: %v", err)
	}
	t.Cleanup(func() { _ = plugin.Post(hc, agent.PhaseRun) })
}

func TestRelocates(t *testing.T) {
	tests := []struct {
		name     string
		checkout *shell.Checkout
		want     bool
	}{
		{name: "nil"},
		{name: "explicitly none", checkout: &shell.Checkout{Mode: shell.CheckoutNone, URL: "https://example.com/x.git"}},
		{name: "empty checkout"},
		{name: "inferred remote", checkout: &shell.Checkout{URL: "https://example.com/x.git"}, want: true},
		{name: "inferred local", checkout: &shell.Checkout{Path: "/work/repo"}, want: true},
		{name: "explicit local", checkout: &shell.Checkout{Mode: shell.CheckoutLocal}, want: true},
		{name: "worktree none", checkout: &shell.Checkout{Worktree: &shell.Worktree{Mode: shell.WorktreeNone}}},
		{name: "worktree new", checkout: &shell.Checkout{Worktree: &shell.Worktree{Mode: shell.WorktreeNew}}, want: true},
		{name: "worktree existing", checkout: &shell.Checkout{Worktree: &shell.Worktree{Mode: shell.WorktreeExisting, Path: "/work/wt"}}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := setup.Relocates(test.checkout); got != test.want {
				t.Errorf("Relocates() = %v, want %v", got, test.want)
			}
		})
	}
}
