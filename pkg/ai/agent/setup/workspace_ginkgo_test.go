package setup_test

import (
	"path/filepath"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("specified workspaces", func() {
	It("records a linked worktree supplied as the execution directory without owning its cleanup", func() {
		repo := sourceRepo()
		path := filepath.Join(GinkgoT().TempDir(), "specified")
		gitIn(repo, "worktree", "add", "-b", "feature/specified", path)
		DeferCleanup(func() { gitIn(repo, "worktree", "remove", "--force", path) })
		path, err := filepath.EvalSymlinks(path)
		Expect(err).NotTo(HaveOccurred())
		base := gitIn(path, "rev-parse", "HEAD")
		plugin := &setup.Plugin{}
		hc := &agent.HookContext{Request: &ai.Request{Setup: &shell.Setup{Cwd: path}}, Response: &ai.Response{}}
		Expect(plugin.PreRun(hc)).To(Succeed())
		Expect(hc.Workspace().Worktree).To(Equal(&api.WorktreeState{
			Repo: repo, Path: path, Branch: "feature/specified", Base: base, Setup: base,
		}))
		Expect(plugin.Post(hc, agent.PhaseRun)).To(Succeed())
		Expect(path).To(BeADirectory())
	})

	It("does not mark a primary checkout or a non-Git directory as an isolated worktree", func() {
		for _, path := range []string{sourceRepo(), GinkgoT().TempDir()} {
			workspace, err := setup.InspectWorkspace(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(workspace).To(Equal(&api.WorkspaceRecord{Cwd: path}))
		}
	})
})
