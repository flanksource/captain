package commit

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

// The source checkout ignores .npmrc, but the branch the worktree is cut from
// predates that rule: the setup clones .npmrc in and the worktree's own
// .gitignore does not hide it, so only the workspace's excludes keep it out.
var _ = Describe("worktree stage mode", func() {
	It("never commits content cloned from the source's gitignored files", func() {
		repo, err := filepath.Abs(newRepo(GinkgoT()))
		Expect(err).NotTo(HaveOccurred())
		mustGit(GinkgoT(), repo, "branch", "old")
		write(GinkgoT(), repo, ".gitignore", ".npmrc\n")
		mustGit(GinkgoT(), repo, "add", ".gitignore")
		mustGit(GinkgoT(), repo, "commit", "-m", "chore: ignore .npmrc")
		write(GinkgoT(), repo, ".npmrc", "//registry.example.com/:_authToken=not-a-real-token\n")

		plugin := &setup.Plugin{BaseDir: GinkgoT().TempDir()}
		hc := &agent.HookContext{
			Context: context.Background(),
			Request: &ai.Request{Prompt: api.Prompt{User: testPrompt}, Setup: &shell.Setup{Checkout: &shell.Checkout{
				Mode: shell.CheckoutLocal, Path: repo,
				Worktree: &shell.Worktree{Mode: shell.WorktreeNew, Base: "old", Uncommitted: shell.CloneSkip, Ignored: shell.CloneClone},
			}}},
			Response: &ai.Response{Workspace: &api.Workspace{}},
			Hooks:    []any{plugin},
		}
		Expect(plugin.PreRun(hc)).To(Succeed())
		DeferCleanup(func() { _ = plugin.Post(hc, agent.PhaseRun) })
		wt := hc.Workspace().Cwd
		Expect(filepath.Join(wt, ".npmrc")).To(BeARegularFile())
		write(GinkgoT(), wt, "agent.go", "package main\n")

		h := New(api.Commit{On: api.CommitOnRun, Stage: api.CommitStageWorktree, Message: "feat: agent work"})
		Expect(h.Post(hc, agent.PhaseRun)).To(Succeed())

		Expect(mustGit(GinkgoT(), wt, "show", "--name-only", "--format=", "HEAD")).To(Equal("agent.go"))
	})
})
