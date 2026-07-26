package claude

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("worktree metadata", func() {
	It("does not treat a marker symlink outside the project as a match", func() {
		root := GinkgoT().TempDir()
		project := filepath.Join(root, "project")
		Expect(os.MkdirAll(project, 0o755)).To(Succeed())
		outside := filepath.Join(root, "outside-go.mod")
		Expect(os.WriteFile(outside, []byte("module example.com/outside\n"), 0o600)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(project, "go.mod"))).To(Succeed())

		Expect(FindProjectInfo(project)).NotTo(SatisfyAll(
			HaveField("Root", project),
			HaveField("MarkerFile", "go.mod"),
		))
	})

	It("does not follow a .git symlink outside the worktree", func() {
		root := GinkgoT().TempDir()
		mainRepo := filepath.Join(root, "main")
		gitDir := filepath.Join(mainRepo, ".git", "worktrees", "feature")
		Expect(os.MkdirAll(gitDir, 0o755)).To(Succeed())

		worktree := filepath.Join(root, "worktree")
		Expect(os.MkdirAll(worktree, 0o755)).To(Succeed())
		outside := filepath.Join(root, "outside-git-file")
		Expect(os.WriteFile(outside, []byte("gitdir: "+gitDir+"\n"), 0o600)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(worktree, ".git"))).To(Succeed())

		Expect(resolveWorktreeRoot(filepath.Join(worktree, ".git"))).To(BeEmpty())
	})
})
