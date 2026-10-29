package commit

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("the workspace commit record", func() {
	var (
		dir string
		hc  *agent.HookContext
	)
	BeforeEach(func() {
		dir = newRepo(GinkgoT())
		hc = isolated(dir)
	})

	headRecord := func(repo, message string) api.CommitRecord {
		return api.CommitRecord{SHA: mustGit(GinkgoT(), repo, "rev-parse", "HEAD"), Message: message}
	}
	turnOn := func(h *Hook, rel string) {
		write(GinkgoT(), dir, rel, "package main\n")
		changed(hc, rel)
		Expect(h.Post(hc, agent.PhaseTurn)).To(Succeed())
	}

	It("records the built-in committer's commit with the message git stored, not the policy text", func() {
		// A YAML block scalar leaves a trailing newline git's cleanup strips.
		h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: built in\n"})
		write(GinkgoT(), dir, "built.go", "package main\n")

		Expect(h.Post(hc, agent.PhaseAgent)).To(Succeed())
		Expect(hc.Workspace().Commits).To(Equal([]api.CommitRecord{headRecord(dir, "feat: built in")}))
	})

	It("records a root commit cut on a repository with no history", func() {
		root := newRepoWithoutCommits(GinkgoT())
		rootHC := isolated(root)
		h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: first"})
		write(GinkgoT(), root, "first.go", "package main\n")

		Expect(h.Post(rootHC, agent.PhaseAgent)).To(Succeed())
		Expect(rootHC.Workspace().Commits).To(Equal([]api.CommitRecord{headRecord(root, "feat: first")}))
	})

	It("rewrites the record to the squashed commit once a per-turn chain collapses, keeping other entries", func() {
		other := api.CommitRecord{SHA: "feedfacefeedfacefeedfacefeedfacefeedface", Message: "chore: another hook"}
		hc.Workspace().AddCommit(other.SHA, other.Message)
		h := New(api.Commit{On: api.CommitOnTurn, Message: "feat: add greeting"})
		turnOn(h, "one.go")
		turnOn(h, "two.go")

		Expect(hc.Workspace().Commits).To(HaveLen(3))
		Expect(hc.Workspace().Commits[2].Message).To(Equal("fixup! feat: add greeting"))
		Expect(h.Post(hc, agent.PhaseAgent)).To(Succeed())
		Expect(h.Post(hc, agent.PhaseRun)).To(Succeed())
		Expect(hc.Workspace().Commits).To(Equal([]api.CommitRecord{other, headRecord(dir, "feat: add greeting")}))
	})

	It("rewrites the record when amend mode folds a later turn into its commit", func() {
		h := New(api.Commit{On: api.CommitOnTurn, Mode: api.CommitModeAmend, Message: "feat: amended"})
		turnOn(h, "one.go")
		turnOn(h, "two.go")

		Expect(hc.Workspace().Commits).To(Equal([]api.CommitRecord{headRecord(dir, "feat: amended")}))
	})
})
