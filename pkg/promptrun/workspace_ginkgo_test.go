package promptrun_test

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("active run workspace reconciliation", func() {
	It("records an existing linked worktree without changing execution, staged files, or inventing the setup revision", func(ctx SpecContext) {
		db := openRecordDB()
		repo := GinkgoT().TempDir()
		path := filepath.Join(GinkgoT().TempDir(), "active")
		git := func(dir string, args ...string) string {
			GinkgoHelper()
			command := exec.Command("git", args...)
			command.Dir = dir
			out, err := command.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "%s", out)
			return string(out)
		}
		git(repo, "init", "-q", "-b", "main", "--template=")
		git(repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "chore: seed")
		git(repo, "worktree", "add", "-b", "feature/active", path)
		DeferCleanup(func() { git(repo, "worktree", "remove", "--force", path) })
		Expect(os.WriteFile(filepath.Join(path, "change.txt"), []byte("work in progress\n"), 0o600)).To(Succeed())
		git(path, "add", "change.txt")
		before := git(path, "status", "--porcelain")
		session, err := db.CreateOrGetSession(ctx, hostTree()[0])
		Expect(err).NotTo(HaveOccurred())
		run, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{
			SessionID: session.ID, RenderedSpec: map[string]any{"setup": map[string]any{"cwd": path}},
		})
		Expect(err).NotTo(HaveOccurred())
		state := database.PromptRunStateRunning
		run, err = db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{ID: run.ID, ExpectedVersion: run.Version, State: &state})
		Expect(err).NotTo(HaveOccurred())
		Expect(promptrun.ReconcileWorkspace(ctx, db, run)).To(Succeed())
		Expect(run.Workspace.Worktree.Branch).To(Equal("feature/active"))
		Expect(run.Workspace.Worktree.Setup).To(BeEmpty())
		Expect(run.Workspace.Worktree.Base).To(BeEmpty())
		Expect(run.Workspace.Worktree.Head).NotTo(BeEmpty())
		stored, err := db.GetPromptRun(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Workspace).To(Equal(run.Workspace))
		Expect(stored.State).To(Equal(state))
		version := run.Version
		Expect(promptrun.ReconcileWorkspace(ctx, db, run)).To(Succeed())
		Expect(run.Version).To(Equal(version))
		Expect(git(path, "status", "--porcelain")).To(Equal(before))
	})
})
