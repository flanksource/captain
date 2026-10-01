package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Workspace.ReplaceCommits", func() {
	var (
		before   = api.CommitRecord{SHA: "aaaa", Message: "chore: before"}
		anchor   = api.CommitRecord{SHA: "bbbb", Message: "feat: anchor"}
		fixup    = api.CommitRecord{SHA: "cccc", Message: "fixup! feat: anchor"}
		after    = api.CommitRecord{SHA: "dddd", Message: "chore: after"}
		squashed = api.CommitRecord{SHA: "eeee", Message: "feat: anchor"}
	)
	workspace := func(commits ...api.CommitRecord) *api.Workspace {
		return &api.Workspace{Commits: append([]api.CommitRecord(nil), commits...)}
	}

	It("puts the replacement where the first replaced entry stood, keeping the rest in order", func() {
		ws := workspace(before, anchor, after, fixup)
		ws.ReplaceCommits([]string{anchor.SHA, fixup.SHA}, []api.CommitRecord{squashed})
		Expect(ws.Commits).To(Equal([]api.CommitRecord{before, squashed, after}))
	})

	It("appends the replacement when none of the old SHAs are recorded", func() {
		ws := workspace(before)
		ws.ReplaceCommits([]string{anchor.SHA}, []api.CommitRecord{squashed})
		Expect(ws.Commits).To(Equal([]api.CommitRecord{before, squashed}))
	})

	It("drops the old entries when the replacement is empty", func() {
		ws := workspace(before, anchor, after)
		ws.ReplaceCommits([]string{anchor.SHA}, nil)
		Expect(ws.Commits).To(Equal([]api.CommitRecord{before, after}))
	})

	It("is a no-op on a nil workspace", func() {
		var ws *api.Workspace
		Expect(func() { ws.ReplaceCommits([]string{anchor.SHA}, []api.CommitRecord{squashed}) }).NotTo(Panic())
	})
})
