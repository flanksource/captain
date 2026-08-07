package aichat

import (
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("projectSessionAgents", func() {
	rootID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	childID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	grandchildID := uuid.MustParse("33333333-3333-4333-8333-333333333333")

	explorer := "Explore"
	rows := []database.SessionAgent{
		{SessionID: rootID, IsRoot: true, InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CostUSD: 0.5},
		{SessionID: childID, ParentSessionID: &rootID, AgentType: &explorer, InputTokens: 40},
		{SessionID: grandchildID, ParentSessionID: &childID},
	}

	ginkgo.It("returns nothing for a session with no agent rows", func() {
		root, agents := projectSessionAgents(nil)

		Expect(root).To(BeNil())
		Expect(agents).To(BeEmpty())
	})

	ginkgo.It("nests each agent under its parent", func() {
		root, agents := projectSessionAgents(rows)

		Expect(agents).To(HaveLen(3))
		Expect(root.ID).To(Equal(rootID.String()))
		Expect(root.Children).To(HaveLen(1))
		Expect(root.Children[0].ID).To(Equal(childID.String()))
		Expect(root.Children[0].Type).To(Equal("Explore"))
		Expect(root.Children[0].Children[0].ID).To(Equal(grandchildID.String()))
	})

	ginkgo.It("carries each agent's own usage and cost", func() {
		root, _ := projectSessionAgents(rows)

		Expect(root.Usage.InputTokens).To(Equal(100))
		Expect(root.Cost.TotalTokens).To(Equal(110))
		Expect(root.Cost.ProviderCostUSD).To(Equal(0.5))
	})

	ginkgo.It("keeps an orphan in the flat index rather than dropping it", func() {
		missingParent := uuid.MustParse("44444444-4444-4444-8444-444444444444")

		_, agents := projectSessionAgents([]database.SessionAgent{
			{SessionID: childID, ParentSessionID: &missingParent},
		})

		Expect(agents).To(HaveLen(1))
		Expect(agents[0].ParentID).To(Equal(missingParent.String()))
	})

	ginkgo.It("does not make a self-parented row its own child", func() {
		root, _ := projectSessionAgents([]database.SessionAgent{
			{SessionID: rootID, ParentSessionID: &rootID, IsRoot: true},
		})

		Expect(root.Children).To(BeEmpty())
	})
})

var _ = ginkgo.Describe("planFromNative", func() {
	approved := database.Plan{
		Path: "/plans/approved.md", Slug: "approved",
		ApprovedRevision: &database.PlanRevision{PlanMarkdown: "# approved"},
		LatestRevision:   &database.PlanRevision{PlanMarkdown: "# newer draft"},
	}
	draft := database.Plan{
		Path: "/plans/draft.md", Slug: "draft",
		LatestRevision: &database.PlanRevision{PlanMarkdown: "# draft"},
	}

	ginkgo.It("has no plan when the session never persisted one", func() {
		Expect(planFromNative(nil)).To(BeNil())
	})

	ginkgo.It("prefers the approved revision over the newer draft on the same plan", func() {
		Expect(planFromNative([]database.Plan{approved})).To(Equal(&session.Plan{
			Path: "/plans/approved.md", Slug: "approved", Content: "# approved", Explicit: true,
		}))
	})

	ginkgo.It("prefers an approved plan over a newer unapproved one", func() {
		// ListPlans orders newest first, so the draft is the more recent row.
		Expect(planFromNative([]database.Plan{draft, approved}).Slug).To(Equal("approved"))
	})

	ginkgo.It("falls back to the latest revision when nothing was approved", func() {
		Expect(planFromNative([]database.Plan{draft}).Content).To(Equal("# draft"))
	})

	ginkgo.It("ignores a plan row that has no revision at all", func() {
		Expect(planFromNative([]database.Plan{{Path: "/plans/empty.md"}})).To(BeNil())
	})
})
