package load_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

var _ = Describe("Projection", func() {
	facts := load.ProjectionFacts{
		Metadata: session.Metadata{
			Model: "stored-model", Provider: "stored-provider",
			Files: session.ChangedFiles{Written: []string{"stored.go"}},
			Todos: []tools.TodoItem{{Text: "ship the slice", Status: "in_progress"}},
			Plan:  &session.Plan{Slug: "metadata-plan"},
		},
		Git:     session.GitState{Branch: "feat/session-load"},
		Context: &session.Context{UsedTokens: 1200, WindowTokens: 4000, FreePercent: 70},
	}

	It("fills the stored row's projection for a session built from any source", func() {
		result, failures := load.Load(context.Background(), load.Projection(facts))

		Expect(failures).To(BeEmpty())
		Expect(result.Session.Model).To(Equal("stored-model"))
		Expect(result.Session.Provider).To(Equal("stored-provider"))
		Expect(result.Session.Files.Written).To(Equal([]string{"stored.go"}))
		Expect(result.Session.Todos).To(HaveLen(1))
		Expect(result.Session.Plan.Slug).To(Equal("metadata-plan"))
		Expect(result.Session.Git.Branch).To(Equal("feat/session-load"))
		Expect(result.Session.Context.FreePercent).To(Equal(70))
	})

	It("keeps a transcript-derived value, which is fresher than the stored copy", func() {
		result, _ := load.Load(context.Background(),
			load.Transcript(&session.Session{
				Model: "transcript-model",
				Git:   session.GitState{Branch: "transcript-branch"},
				Files: session.ChangedFiles{Written: []string{"transcript.go"}},
			}),
			load.Projection(facts))

		Expect(result.Session.Model).To(Equal("transcript-model"))
		Expect(result.Session.Git.Branch).To(Equal("transcript-branch"))
		Expect(result.Session.Files.Written).To(Equal([]string{"transcript.go"}))
	})

	It("lets the approved plan outrank whatever the branch already found", func() {
		approved := &session.Plan{Slug: "approved-plan", Content: "# plan", Explicit: true}
		withPlan := facts
		withPlan.Plan = approved

		result, _ := load.Load(context.Background(),
			load.Transcript(&session.Session{Plan: &session.Plan{Slug: "transcript-plan"}}),
			load.Projection(withPlan))

		Expect(result.Session.Plan).To(Equal(approved))
	})

	It("rolls descendant files up into a parent that edited nothing itself", func() {
		withThread := facts
		withThread.Metadata.Files = session.ChangedFiles{}
		withThread.ThreadFiles = &session.ChangedFiles{
			Read: []string{"b.go", "a.go", "a.go"}, Written: []string{"c.go"},
		}

		result, _ := load.Load(context.Background(),
			load.Transcript(&session.Session{Files: session.ChangedFiles{Written: []string{"own.go"}}}),
			load.Projection(withThread))

		Expect(result.Session.Files.Read).To(Equal([]string{"a.go", "b.go"}))
		Expect(result.Session.Files.Written).To(Equal([]string{"c.go", "own.go"}))
	})

	It("keeps a leaf's own file set when the thread has no descendants", func() {
		own := session.ChangedFiles{Written: []string{"own.go"}}
		result, _ := load.Load(context.Background(),
			load.Transcript(&session.Session{Files: own}), load.Projection(facts))

		Expect(result.Session.Files).To(Equal(own))
	})

	It("does not project approvals, which the stored copy overcounts", func() {
		withApprovals := facts
		withApprovals.Metadata.Approvals = session.ApprovalStats{Approved: 200}

		result, _ := load.Load(context.Background(), load.Projection(withApprovals))

		Expect(result.Session.Approvals).To(Equal(session.ApprovalStats{}))
		Expect(result.Provenance.Of(load.FacetApprovals)).To(Equal(load.SourceNone))
	})

	It("treats an empty projection as having nothing to say", func() {
		result, failures := load.Load(context.Background(), load.Projection(load.ProjectionFacts{}))

		Expect(failures).To(BeEmpty())
		Expect(*result.Session).To(Equal(session.Session{}))
	})
})
