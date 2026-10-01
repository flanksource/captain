package commit

import (
	"fmt"

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

	// hostCommit is a host pipeline cutting one commit under its own message —
	// the way gavel's commit pipeline does, ignoring Plan.Subject.
	hostCommit := func(repo, rel, message string) string {
		write(GinkgoT(), repo, rel, "package main\n")
		mustGit(GinkgoT(), repo, "add", "--", rel)
		mustGit(GinkgoT(), repo, "commit", "--no-verify", "-m", message)
		return mustGit(GinkgoT(), repo, "rev-parse", "HEAD")
	}
	headRecord := func(repo, message string) api.CommitRecord {
		return api.CommitRecord{SHA: mustGit(GinkgoT(), repo, "rev-parse", "HEAD"), Message: message}
	}
	turnOn := func(h *Hook, rel string) {
		write(GinkgoT(), dir, rel, "package main\n")
		changed(hc, rel)
		Expect(h.Post(hc, agent.PhaseTurn)).To(Succeed())
	}

	It("records every commit a host Do cuts, in order, with the messages git holds", func() {
		messages := []string{"feat: one", "fix: two\n\nwith a body"}
		var want []api.CommitRecord
		h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: captain subject"})
		h.Do = func(_ *agent.HookContext, plan Plan) (string, error) {
			for i, message := range messages {
				sha := hostCommit(plan.Dir, fmt.Sprintf("host-%d.go", i), message)
				want = append(want, api.CommitRecord{SHA: sha, Message: message})
			}
			// A host may hand back an abbreviated hash for the anchor.
			return want[len(want)-1].SHA[:7], nil
		}
		write(GinkgoT(), dir, "agent.go", "package main\n")

		Expect(h.Post(hc, agent.PhaseAgent)).To(Succeed())
		Expect(hc.Workspace().Commits).To(Equal(want))
	})

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

	It("fails naming the SHA and range when Do returns a SHA it did not cut", func() {
		const stray = "0123456789abcdef0123456789abcdef01234567"
		seed := mustGit(GinkgoT(), dir, "rev-parse", "HEAD")
		h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: hosted"})
		h.Do = func(_ *agent.HookContext, plan Plan) (string, error) {
			hostCommit(plan.Dir, "host.go", "feat: hosted")
			return stray, nil
		}
		write(GinkgoT(), dir, "agent.go", "package main\n")

		err := h.Post(hc, agent.PhaseAgent)
		Expect(err).To(MatchError(And(ContainSubstring(stray), ContainSubstring(seed+"..HEAD"))))
		Expect(hc.Workspace().Commits).To(BeEmpty())
	})

	It("fails when Do commits but reports no SHA, instead of recording nothing", func() {
		seed := mustGit(GinkgoT(), dir, "rev-parse", "HEAD")
		h := New(api.Commit{On: api.CommitOnAgent, Message: "feat: hosted"})
		var cut string
		h.Do = func(_ *agent.HookContext, plan Plan) (string, error) {
			cut = hostCommit(plan.Dir, "host.go", "feat: hosted")
			return "", nil
		}
		write(GinkgoT(), dir, "agent.go", "package main\n")

		err := h.Post(hc, agent.PhaseAgent)
		Expect(err).To(MatchError(And(ContainSubstring("reported no commit"), ContainSubstring(seed), ContainSubstring(cut))))
		Expect(hc.Workspace().Commits).To(BeEmpty())
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
