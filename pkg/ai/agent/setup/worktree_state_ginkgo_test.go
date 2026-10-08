package setup_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons/merge"
)

const snapshotSubject = "chore(setup): snapshot uncommitted changes"

// gitIn runs git in dir and returns its trimmed stdout, failing the spec on a
// non-zero exit.
func gitIn(dir string, args ...string) string {
	GinkgoHelper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func writeFile(dir, rel, body string) {
	GinkgoHelper()
	path := filepath.Join(dir, rel)
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
}

// sourceRepo is a real repo with one commit and a .gitignore hiding cache/, so a
// spec can put tracked, untracked and ignored content in front of the worktree
// setup. The developer's global excludes and signing config are neutralised so
// git reports exactly what the spec wrote.
func sourceRepo() string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	gitIn(dir, "init", "-q", "-b", "main", "--template=")
	gitIn(dir, "config", "user.email", "captain-test@example.com")
	gitIn(dir, "config", "user.name", "Captain Test")
	gitIn(dir, "config", "commit.gpgsign", "false")
	gitIn(dir, "config", "core.excludesFile", "/dev/null")
	writeFile(dir, "README.md", "seed\n")
	writeFile(dir, ".gitignore", "cache/\n")
	gitIn(dir, "add", "README.md", ".gitignore")
	gitIn(dir, "commit", "-q", "-m", "chore: seed")
	resolved, err := filepath.EvalSymlinks(dir)
	Expect(err).NotTo(HaveOccurred())
	return resolved
}

// worktreeHook builds the hook context and plugin for a run the setup moves into
// a new worktree of repo that carries the source's uncommitted and ignored
// content across.
func worktreeHook(repo string) (*setup.Plugin, *agent.HookContext) {
	req := &ai.Request{Setup: &shell.Setup{Checkout: &shell.Checkout{
		Mode: shell.CheckoutLocal, Path: repo,
		Worktree: &shell.Worktree{Mode: shell.WorktreeNew, Uncommitted: shell.CloneClone, Ignored: shell.CloneClone},
	}}}
	plugin := &setup.Plugin{BaseDir: GinkgoT().TempDir()}
	return plugin, &agent.HookContext{
		Context:  context.Background(),
		Request:  req,
		Original: merge.Clone(*req, api.MergePolicy()),
		Response: &ai.Response{Workspace: &api.Workspace{}},
		Hooks:    []any{plugin},
	}
}

// preRun prepares the worktree and registers a teardown that removes whatever
// the spec left behind, so a kept worktree never outlives the spec.
func preRun(plugin *setup.Plugin, hc *agent.HookContext, repo string) *api.WorktreeState {
	GinkgoHelper()
	Expect(plugin.PreRun(hc)).To(Succeed())
	wt := hc.Workspace().Worktree
	Expect(wt).NotTo(BeNil(), "a worktree run records its worktree state")
	path := wt.Path
	DeferCleanup(func() {
		if _, err := os.Stat(path); err == nil {
			gitIn(repo, "worktree", "remove", "--force", path)
		}
	})
	return wt
}

func branchExists(repo, branch string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repo
	return cmd.Run() == nil
}

var _ = Describe("the setup plugin's worktree state", func() {
	var repo string

	BeforeEach(func() {
		if _, err := exec.LookPath("git"); err != nil {
			Fail("git is required for the setup worktree specs")
		}
		repo = sourceRepo()
	})

	It("records repo/branch/base on PreRun", func() {
		base := gitIn(repo, "rev-parse", "HEAD")
		plugin, hc := worktreeHook(repo)

		wt := preRun(plugin, hc, repo)

		Expect(wt.Branch).To(HavePrefix("shell/"))
		Expect(*wt).To(Equal(api.WorktreeState{
			Repo: repo, Path: hc.Workspace().Cwd, Branch: wt.Branch, Base: base, Setup: base,
		}))
		Expect(gitIn(wt.Path, "symbolic-ref", "--short", "HEAD")).To(Equal(wt.Branch))
		Expect(noticeTexts(hc)).To(Equal([]string{
			"[pre-run] worktree " + wt.Path + " on " + wt.Branch + " from " + base[:7],
		}))
	})

	It("snapshots copied WIP as a setup commit and records Setup", func() {
		base := gitIn(repo, "rev-parse", "HEAD")
		writeFile(repo, "README.md", "seed\nwork in progress\n")
		writeFile(repo, "draft.txt", "untracked draft\n")
		writeFile(repo, "cache/build.bin", "ignored output\n")
		plugin, hc := worktreeHook(repo)

		wt := preRun(plugin, hc, repo)

		Expect(wt.Base).To(Equal(base))
		Expect(wt.Setup).NotTo(Equal(base), "the copied WIP is committed as setup")
		Expect(gitIn(wt.Path, "rev-parse", "HEAD")).To(Equal(wt.Setup))
		Expect(gitIn(wt.Path, "rev-parse", wt.Setup+"^")).To(Equal(base))
		Expect(gitIn(wt.Path, "log", "-1", "--format=%B", wt.Setup)).To(Equal(snapshotSubject + "\n\nCaptain-Setup: true"))
		Expect(strings.Split(gitIn(wt.Path, "show", "--name-only", "--format=", wt.Setup), "\n")).
			To(ConsistOf("README.md", "draft.txt"), "gitignored content is copied but never committed")
		Expect(filepath.Join(wt.Path, "cache", "build.bin")).To(BeARegularFile())
		Expect(gitIn(wt.Path, "status", "--porcelain")).To(BeEmpty(), "the agent starts from a clean tree")
		Expect(gitIn(repo, "status", "--porcelain")).NotTo(BeEmpty(), "the source checkout is never touched")
	})

	It("no setup commit when the copied tree is clean", func() {
		base := gitIn(repo, "rev-parse", "HEAD")
		writeFile(repo, "cache/build.bin", "ignored output\n")
		plugin, hc := worktreeHook(repo)

		wt := preRun(plugin, hc, repo)

		Expect(wt.Setup).To(Equal(base))
		Expect(gitIn(wt.Path, "rev-parse", "HEAD")).To(Equal(base))
	})

	It("Post keeps a dirty worktree and reports dirty paths without force-removing", func() {
		plugin, hc := worktreeHook(repo)
		wt := preRun(plugin, hc, repo)
		writeFile(wt.Path, "agent.go", "package main\n")
		writeFile(wt.Path, "README.md", "edited by the agent\n")

		Expect(plugin.Post(hc, agent.PhaseRun)).To(Succeed())

		Expect(wt.Kept).To(BeTrue())
		Expect(wt.KeptReason).To(Equal("uncommitted changes"))
		Expect(wt.Dirty).To(ConsistOf("agent.go", "README.md"))
		Expect(wt.Removed).To(BeFalse())
		Expect(wt.BranchDeleted).To(BeFalse())
		Expect(wt.Head).To(Equal(wt.Setup))
		Expect(filepath.Join(wt.Path, "agent.go")).To(BeARegularFile(), "the agent's edits survive teardown")
		Expect(branchExists(repo, wt.Branch)).To(BeTrue())
		Expect(noticeTexts(hc)).To(ContainElement(
			"[post-run] kept worktree " + wt.Path + " on " + wt.Branch + ": uncommitted changes"))
	})

	It("Post removes a clean worktree and deletes a branch with no commits past Setup", func() {
		writeFile(repo, "draft.txt", "untracked draft\n")
		plugin, hc := worktreeHook(repo)
		wt := preRun(plugin, hc, repo)
		Expect(wt.Setup).NotTo(Equal(wt.Base), "the only commit on the branch is the setup snapshot")

		Expect(plugin.Post(hc, agent.PhaseRun)).To(Succeed())

		Expect(wt.Kept).To(BeFalse())
		Expect(wt.Removed).To(BeTrue())
		Expect(wt.BranchDeleted).To(BeTrue())
		Expect(wt.Head).To(Equal(wt.Setup))
		Expect(wt.Path).NotTo(BeADirectory())
		Expect(branchExists(repo, wt.Branch)).To(BeFalse())
		Expect(noticeTexts(hc)).To(ContainElement(
			"[post-run] removed worktree " + wt.Path + "; deleted branch " + wt.Branch))
	})

	It("Post removes a clean worktree but keeps a branch with commits", func() {
		plugin, hc := worktreeHook(repo)
		wt := preRun(plugin, hc, repo)
		writeFile(wt.Path, "agent.go", "package main\n")
		gitIn(wt.Path, "add", "agent.go")
		gitIn(wt.Path, "commit", "-q", "-m", "feat: agent work")
		head := gitIn(wt.Path, "rev-parse", "HEAD")

		Expect(plugin.Post(hc, agent.PhaseRun)).To(Succeed())

		Expect(wt.Removed).To(BeTrue())
		Expect(wt.BranchDeleted).To(BeFalse())
		Expect(wt.Head).To(Equal(head))
		Expect(wt.Path).NotTo(BeADirectory())
		Expect(gitIn(repo, "rev-parse", "refs/heads/"+wt.Branch)).To(Equal(head), "the branch still holds the agent's commit")
		Expect(noticeTexts(hc)).To(ContainElement(
			"[post-run] removed worktree " + wt.Path + "; branch " + wt.Branch + " @ " + head[:7]))
	})

	It("continues on an existing branch and never deletes it, even with no new commits", func() {
		gitIn(repo, "branch", "shell/previous")
		gitIn(repo, "checkout", "-q", "shell/previous")
		writeFile(repo, "previous.go", "package main\n")
		gitIn(repo, "add", "previous.go")
		gitIn(repo, "commit", "-q", "-m", "feat: previous run")
		tip := gitIn(repo, "rev-parse", "HEAD")
		gitIn(repo, "checkout", "-q", "main")
		plugin, hc := worktreeHook(repo)
		hc.Request.Setup.Checkout.Worktree = &shell.Worktree{Mode: shell.WorktreeBranch, Branch: "shell/previous"}

		wt := preRun(plugin, hc, repo)
		Expect(wt.Branch).To(Equal("shell/previous"))
		Expect(wt.Base).To(Equal(tip))
		Expect(wt.Setup).To(Equal(tip), "the agent's own work starts at the previous run's tip")

		Expect(plugin.Post(hc, agent.PhaseRun)).To(Succeed())

		Expect(wt.Removed).To(BeTrue())
		Expect(wt.BranchDeleted).To(BeFalse())
		Expect(gitIn(repo, "rev-parse", "refs/heads/shell/previous")).To(Equal(tip), "the previous run's commits survive")
	})
})
