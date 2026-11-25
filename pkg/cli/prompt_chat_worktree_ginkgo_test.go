package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const resumeTestBranch = "gavel-pr/previous"

func resumeTestGit(dir string, args ...string) string {
	GinkgoHelper()
	command := exec.Command("git", append([]string{
		"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false",
	}, args...)...)
	command.Dir = dir
	out, err := command.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// removedWorktree is a repository whose run worktree was removed, as teardown
// leaves it: the branch, one commit ahead, survives.
func removedWorktree() api.WorktreeState {
	GinkgoHelper()
	root, err := filepath.EvalSymlinks(GinkgoT().TempDir())
	Expect(err).NotTo(HaveOccurred())
	repo, path := filepath.Join(root, "repo"), filepath.Join(root, "worktrees", "previous")
	Expect(os.Mkdir(repo, 0o755)).To(Succeed())
	resumeTestGit(repo, "init", "-q", "-b", "main", "--template=")
	resumeTestGit(repo, "commit", "--allow-empty", "-qm", "chore: seed")
	resumeTestGit(repo, "worktree", "add", "-q", "-b", resumeTestBranch, path)
	resumeTestGit(path, "commit", "--allow-empty", "-qm", "feat: previous run")
	head := resumeTestGit(path, "rev-parse", "HEAD")
	resumeTestGit(repo, "worktree", "remove", path)
	return api.WorktreeState{Repo: repo, Path: path, Branch: resumeTestBranch, Head: head, Removed: true}
}

// recreatedWorktree applies a resume setup and returns the commit the worktree
// it recreated has checked out.
func recreatedWorktree(ctx SpecContext, prepared *shell.Setup, state api.WorktreeState) string {
	GinkgoHelper()
	Expect(prepared).To(Equal(&shell.Setup{Cwd: state.Repo, Checkout: &shell.Checkout{
		Mode: shell.CheckoutLocal, Path: state.Repo,
		Worktree: &shell.Worktree{
			Mode: shell.WorktreeBranch, Branch: state.Branch, Path: state.Path, Keep: true,
			Uncommitted: shell.CloneSkip, Ignored: shell.CloneClone,
		},
	}}))
	req := ai.Request{Setup: prepared}
	result, err := setup.Apply(ctx, &req, "")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(result.Cleanup)
	Expect(req.Cwd()).To(Equal(state.Path))
	Expect(resumeTestGit(state.Path, "symbolic-ref", "--short", "HEAD")).To(Equal(state.Branch))
	return resumeTestGit(state.Path, "rev-parse", "HEAD")
}

// worktreeUnavailableOf is the recovery offer a refused resume carries.
func worktreeUnavailableOf(err error) any {
	GinkgoHelper()
	var target chatHTTPError
	Expect(errors.As(err, &target)).To(BeTrue(), "%v is not a chat error", err)
	return target.fields["worktree"]
}

var _ = Describe("resuming in a removed worktree", func() {
	It("recreates the worktree from its branch at its original path and keeps it", func(ctx SpecContext) {
		state := removedWorktree()

		prepared, err := resumeWorktreeSetup(ctx, state, "")
		Expect(err).NotTo(HaveOccurred())
		req := ai.Request{Setup: prepared}
		result, err := setup.Apply(ctx, &req, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(req.Cwd()).To(Equal(state.Path))
		Expect(resumeTestGit(state.Path, "rev-parse", "HEAD")).To(Equal(state.Head))

		edit := filepath.Join(state.Path, "edit.txt")
		Expect(os.WriteFile(edit, []byte("uncommitted\n"), 0o600)).To(Succeed())
		Expect(result.Cleanup()).To(Succeed())
		Expect(edit).To(BeAnExistingFile())
	})

	DescribeTable("refuses a worktree that cannot be recreated at all",
		func(ctx SpecContext, change func(*api.WorktreeState), want string) {
			state := removedWorktree()
			change(&state)

			_, err := resumeWorktreeSetup(ctx, state, "")

			Expect(chatErrorStatus(err)).To(Equal(http.StatusUnprocessableEntity))
			Expect(err).To(MatchError(SatisfyAll(ContainSubstring(state.Path), ContainSubstring(want))))
		},
		Entry("the worktree had no branch", func(s *api.WorktreeState) { s.Branch = "" }, "no branch"),
		Entry("the repository is gone", func(s *api.WorktreeState) { s.Repo = filepath.Join(s.Repo, "gone") }, "repository"),
	)

	DescribeTable("offers a new worktree or the session's last commit when its branch is gone",
		func(ctx SpecContext, change func(*api.WorktreeState), want string) {
			state := removedWorktree()
			change(&state)

			_, err := resumeWorktreeSetup(ctx, state, "")

			Expect(chatErrorStatus(err)).To(Equal(http.StatusConflict))
			Expect(err).To(MatchError(SatisfyAll(ContainSubstring(state.Path), ContainSubstring(want))))
			Expect(worktreeUnavailableOf(err)).To(Equal(WorktreeUnavailable{
				Path: state.Path, Branch: state.Branch, Head: state.Head,
				Recoveries: []WorktreeRecovery{WorktreeRecoveryNew, WorktreeRecoveryHead},
			}))
		},
		Entry("teardown deleted the branch", func(s *api.WorktreeState) {
			resumeTestGit(s.Repo, "branch", "-D", resumeTestBranch)
			s.BranchDeleted = true
		}, "branch "+resumeTestBranch+" was deleted"),
		Entry("the branch no longer exists", func(s *api.WorktreeState) {
			resumeTestGit(s.Repo, "branch", "-D", resumeTestBranch)
		}, "branch "+resumeTestBranch+" no longer exists"),
		Entry("only a branch beneath that name exists", func(s *api.WorktreeState) { s.Branch = "gavel-pr" }, "branch gavel-pr no longer exists"),
	)

	Context("whose branch was deleted", func() {
		var state api.WorktreeState
		BeforeEach(func() {
			state = removedWorktree()
			resumeTestGit(state.Repo, "branch", "-D", resumeTestBranch)
		})

		It("offers only a new worktree once the session's last commit is gone", func(ctx SpecContext) {
			state.Head = strings.Repeat("0", len(state.Head))

			_, err := resumeWorktreeSetup(ctx, state, "")

			Expect(chatErrorStatus(err)).To(Equal(http.StatusConflict))
			Expect(worktreeUnavailableOf(err)).To(HaveField("Recoveries", []WorktreeRecovery{WorktreeRecoveryNew}))
		})

		It("starts a new worktree on the branch from the repository's HEAD", func(ctx SpecContext) {
			prepared, err := resumeWorktreeSetup(ctx, state, WorktreeRecoveryNew)

			Expect(err).NotTo(HaveOccurred())
			Expect(recreatedWorktree(ctx, prepared, state)).To(Equal(resumeTestGit(state.Repo, "rev-parse", "HEAD")))
		})

		It("restores the branch at the session's last commit", func(ctx SpecContext) {
			prepared, err := resumeWorktreeSetup(ctx, state, WorktreeRecoveryHead)

			Expect(err).NotTo(HaveOccurred())
			Expect(recreatedWorktree(ctx, prepared, state)).To(Equal(state.Head))
		})

		It("refuses the session's last commit once it is gone", func(ctx SpecContext) {
			state.Head = strings.Repeat("0", len(state.Head))

			_, err := resumeWorktreeSetup(ctx, state, WorktreeRecoveryHead)

			Expect(chatErrorStatus(err)).To(Equal(http.StatusUnprocessableEntity))
			Expect(err).To(MatchError(ContainSubstring(state.Head)))
		})
	})

	It("writes the recovery offer as the JSON body of the 409", func(ctx SpecContext) {
		state := removedWorktree()
		resumeTestGit(state.Repo, "branch", "-D", resumeTestBranch)
		_, refused := resumeWorktreeSetup(ctx, state, "")
		response := httptest.NewRecorder()

		writeChatError(response, refused)

		Expect(response.Code).To(Equal(http.StatusConflict))
		Expect(response.Body.String()).To(MatchJSON(fmt.Sprintf(
			`{"error": %q, "worktree": {"path": %q, "branch": %q, "head": %q, "recoveries": ["new", "head"]}}`,
			refused.Error(), state.Path, state.Branch, state.Head)))
	})

	It("rejects an unknown worktree recovery", func(ctx SpecContext) {
		_, err := resumeWorktreeSetup(ctx, removedWorktree(), "rebase")

		Expect(chatErrorStatus(err)).To(Equal(http.StatusBadRequest))
		Expect(err).To(MatchError(ContainSubstring(`"rebase"`)))
	})

	// An item with no id fails the recorded-run lookup, so success means the
	// lookup never ran.
	It("continues in a directory that is still there without reading recorded runs", func(ctx SpecContext) {
		cwd := GinkgoT().TempDir()

		prepared, err := resumeSetup(ctx, SessionGetItem{}, resumeSetupOptions{CWD: cwd})

		Expect(err).NotTo(HaveOccurred())
		Expect(prepared).To(Equal(&shell.Setup{Cwd: cwd}))
	})

	It("refuses a working directory that is a file", func(ctx SpecContext) {
		path := filepath.Join(GinkgoT().TempDir(), "file")
		Expect(os.WriteFile(path, nil, 0o600)).To(Succeed())

		_, err := resumeSetup(ctx, SessionGetItem{}, resumeSetupOptions{CWD: path})

		Expect(chatErrorStatus(err)).To(Equal(http.StatusUnprocessableEntity))
		Expect(err).To(MatchError(ContainSubstring("not a directory")))
	})
})
