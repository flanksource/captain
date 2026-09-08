package load_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

var _ = Describe("Requests", func() {
	requests := []session.Request{
		{ID: "r1", State: "approved", Tool: "Bash", ToolCallID: "call-1"},
		{ID: "r2", State: "denied", Tool: "Write", ToolCallID: "call-2", Reason: "out of scope"},
		{ID: "r3", State: "pending", Tool: "Bash", ToolCallID: "call-3"},
		{ID: "r4", State: "pending", Tool: "Edit", ToolCallID: "call-4"},
		{ID: "r5", State: "cancelled", Tool: "Bash", ToolCallID: "call-5"},
		{ID: "r6", State: "expired", Tool: "Bash", ToolCallID: "call-6"},
	}

	It("counts every request state, so a run suspended on an approval says so", func() {
		// The measured defect: approvals loaded in only one of three branches, so a
		// run blocked on a tool approval reported {approved:0, denied:0}.
		result, failures := load.Load(context.Background(), load.Requests(requests))

		Expect(failures).To(BeEmpty())
		Expect(result.Session.Approvals).To(Equal(session.ApprovalStats{
			Approved: 1, Denied: 1, Pending: 2, Cancelled: 1, Expired: 1,
			Denials: []session.Denial{{ToolUseID: "call-2", Tool: "Write", Reason: "out of scope"}},
		}))
		Expect(result.Session.Requests).To(Equal(requests))
		Expect(result.Provenance.Of(load.FacetApprovals)).To(Equal(load.SourceRequests))
	})

	It("supplies approvals whichever source won the transcript", func() {
		result, _ := load.Load(context.Background(),
			load.Transcript(&session.Session{
				Messages:  []session.Message{{ID: "transcript-m1"}},
				Approvals: session.ApprovalStats{Approved: 200},
			}),
			load.Requests(requests[2:4]))

		Expect(result.Session.Approvals.Pending).To(Equal(2))
		Expect(result.Session.Approvals.Approved).To(BeZero())
	})

	It("reports an unrecognised state instead of dropping it from the counts", func() {
		result, failures := load.Load(context.Background(),
			load.Requests([]session.Request{{ID: "r9", State: "escalated", Tool: "Bash"}}))

		Expect(failures).To(HaveLen(1))
		Expect(failures[0].Error()).To(ContainSubstring("escalated"))
		Expect(result.Session.Requests).To(HaveLen(1), "the request is still reported")
	})

	It("treats an empty request set as having nothing to say", func() {
		result, failures := load.Load(context.Background(), load.Requests(nil))

		Expect(failures).To(BeEmpty())
		Expect(result.Provenance.Of(load.FacetApprovals)).To(Equal(load.SourceNone))
	})
})
