package aichat

import (
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"
)

var _ = ginkgo.Describe("threadFiles", func() {
	parentID := uuid.MustParse("2d33df99-654b-5b25-9d3e-b4d3a31e7cb5")
	childID := uuid.MustParse("860305d7-e8cd-41b8-b5ee-332dc74d4a41")
	grandchildID := uuid.MustParse("55555555-5555-4555-8555-555555555555")

	filesRow := func(id uuid.UUID, parent *uuid.UUID, written ...string) database.SessionOverview {
		row := database.SessionOverview{ID: id, ParentSessionID: parent}
		blob, err := json.Marshal(map[string]any{
			"files": session.ChangedFiles{Written: written},
		})
		Expect(err).NotTo(HaveOccurred())
		row.Metadata = blob
		return row
	}

	thread := []database.SessionOverview{
		{ID: parentID},
		filesRow(childID, &parentID, "todos/outcome.go", "todos/plans.go"),
		filesRow(grandchildID, &childID, "todos/provider.go", "todos/plans.go"),
	}

	ginkgo.It("gives a parent that edited nothing its sub-agents' files", func() {
		// The reported bug: the thread's parent row carries no files metadata, so
		// selecting it in the hierarchy showed an empty Files tab.
		Expect(threadFiles(parentID, session.ChangedFiles{}, thread).Written).To(Equal(
			[]string{"todos/outcome.go", "todos/plans.go", "todos/provider.go"}))
	})

	ginkgo.It("includes descendants more than one level down", func() {
		Expect(threadFiles(parentID, session.ChangedFiles{}, thread).Written).To(
			ContainElement("todos/provider.go"))
	})

	ginkgo.It("merges the parent's own edits with its descendants'", func() {
		own := session.ChangedFiles{Written: []string{"cmd/gavel/todos_plan.go"}}

		Expect(threadFiles(parentID, own, thread).Written).To(Equal([]string{
			"cmd/gavel/todos_plan.go", "todos/outcome.go", "todos/plans.go", "todos/provider.go",
		}))
	})

	ginkgo.It("leaves a leaf reporting only its own files", func() {
		own := session.ChangedFiles{Written: []string{"todos/provider.go", "todos/plans.go"}}

		// Unsorted and unmerged: a leaf's set is returned untouched so the
		// hierarchy still shows which sub-agent touched what.
		Expect(threadFiles(grandchildID, own, thread)).To(Equal(own))
	})

	ginkgo.It("does not roll a sibling's files onto a leaf", func() {
		Expect(threadFiles(grandchildID, session.ChangedFiles{}, thread).Written).To(BeEmpty())
	})

	ginkgo.It("returns a mid-tree node its own subtree, not the whole thread", func() {
		Expect(threadFiles(childID, session.ChangedFiles{Written: []string{"todos/outcome.go"}}, thread).Written).To(
			Equal([]string{"todos/outcome.go", "todos/plans.go", "todos/provider.go"}))
	})

	ginkgo.It("returns the session's own files when it is the only row", func() {
		own := session.ChangedFiles{Read: []string{"a.go"}}

		Expect(threadFiles(parentID, own, []database.SessionOverview{{ID: parentID}})).To(Equal(own))
	})
})
