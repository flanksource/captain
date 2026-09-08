package load_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

// toolPart is one assistant tool part carrying a durable approval id, the shape
// Captain writes during live ingest.
func toolPart(approvalID, state string) session.Part {
	return session.Part{Type: "tool", State: state, Approval: &session.Approval{ID: approvalID}}
}

func withParts(parts ...session.Part) *session.Session {
	return &session.Session{Messages: []session.Message{{ID: "m1", Role: "assistant", Parts: parts}}}
}

var _ = Describe("tool part reconciliation", func() {
	// The database branch used to do this inline alongside the approval counts.
	// Composition splits those into two facets, so reconciliation runs once after
	// every load — otherwise a transcript-sourced session would show correct
	// counts beside tool parts still frozen in their pre-approval state.
	It("marks a part whose request is still unanswered as awaiting approval", func() {
		aggregate := withParts(toolPart("req-1", session.ToolStateInputAvailable))

		load.Reconcile(aggregate, []session.Request{{ID: "req-1", State: "pending", Tool: "Write"}})

		Expect(aggregate.Messages[0].Parts[0].State).To(Equal(session.ToolStateApprovalRequested))
	})

	It("records the decision and its reason on an answered part", func() {
		aggregate := withParts(toolPart("req-1", session.ToolStateApprovalRequested))

		load.Reconcile(aggregate, []session.Request{
			{ID: "req-1", State: "approved", Tool: "Write", Reason: "looks right"}})

		part := aggregate.Messages[0].Parts[0]
		Expect(part.State).To(Equal(session.ToolStateApprovalResponded))
		Expect(part.Approval.Approved).To(HaveValue(BeTrue()))
		Expect(part.Approval.Reason).To(Equal("looks right"))
	})

	DescribeTable("refusing states all deny the output",
		func(state string) {
			aggregate := withParts(toolPart("req-1", session.ToolStateApprovalRequested))

			load.Reconcile(aggregate, []session.Request{{ID: "req-1", State: state, Reason: "nope"}})

			part := aggregate.Messages[0].Parts[0]
			Expect(part.State).To(Equal(session.ToolStateOutputDenied))
			Expect(part.Approval.Approved).To(HaveValue(BeFalse()))
			Expect(part.Approval.Reason).To(Equal("nope"))
		},
		Entry("denied", "denied"),
		Entry("cancelled", "cancelled"),
	)

	It("leaves a part alone when no request matches it", func() {
		aggregate := withParts(toolPart("req-missing", session.ToolStateInputAvailable))

		load.Reconcile(aggregate, []session.Request{{ID: "req-1", State: "approved"}})

		Expect(aggregate.Messages[0].Parts[0].State).To(Equal(session.ToolStateInputAvailable))
		Expect(aggregate.Messages[0].Parts[0].Approval.Approved).To(BeNil())
	})

	It("leaves parts that carry no approval alone", func() {
		aggregate := withParts(session.Part{Type: "text"})

		Expect(func() {
			load.Reconcile(aggregate, []session.Request{{ID: "req-1", State: "approved"}})
		}).NotTo(Panic())
	})

	It("runs as part of Load, so neither route has to remember it", func() {
		// The point of putting this in Load: a caller that forgets the step is
		// how the two assemblers drifted apart in the first place.
		aggregate := withParts(toolPart("req-1", session.ToolStateInputAvailable))

		result, failures := load.Load(context.Background(),
			load.Database(aggregate),
			load.Requests([]session.Request{{ID: "req-1", State: "pending", Tool: "Write"}}))

		Expect(failures).To(BeEmpty())
		Expect(result.Session.Messages[0].Parts[0].State).To(Equal(session.ToolStateApprovalRequested))
		Expect(result.Session.Approvals.Pending).To(Equal(1))
	})
})
