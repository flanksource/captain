package promptrun

import (
	"context"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

var _ = g.Describe("Direct approval callbacks", func() {
	request := api.ApprovalRequest{Tool: "Edit", Kind: api.ApprovalKindTool, ToolUseID: "toolu_direct",
		Input: map[string]any{"file_path": "a.go"}, LegacyContract: true}
	approve := func(context.Context, api.ApprovalRequest) (api.ApprovalDecision, error) {
		return api.ApprovalDecision{Allow: true}, nil
	}

	g.It("announces each request once through OnEvent before the host's callback answers it", func() {
		var events []ai.Event
		in := Input{Config: api.Config{OnApproval: approve}, OnEvent: func(_ int, event ai.Event) { events = append(events, event) }}
		announceApprovals(&in)

		decision, err := in.Config.OnApproval(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(decision.Allow).To(BeTrue())
		Expect(events).To(HaveLen(1))
		Expect(events[0]).To(And(
			HaveField("Kind", api.EventPermission),
			HaveField("Tool", "Edit"),
			HaveField("ToolCallID", "toolu_direct"),
			HaveField("Input", request.Input),
			HaveField("Request", Equal(&request)),
		))
	})

	g.It("announces through the deprecated CanUseTool field too, leaving legacy routing to provider construction", func() {
		var events []ai.Event
		in := Input{Config: api.Config{CanUseTool: approve}, OnEvent: func(_ int, event ai.Event) { events = append(events, event) }}
		announceApprovals(&in)

		Expect(in.Config.OnApproval).To(BeNil())
		_, err := in.Config.CanUseTool(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(1))
	})

	g.It("leaves a brokered run alone, because the broker announces with the approval id", func() {
		in := Input{Config: api.Config{}, Approvals: &ApprovalOptions{RequestedBy: "gavel-dashboard"}, OnEvent: func(int, ai.Event) {
			g.Fail("a brokered run must not be announced twice")
		}}
		announceApprovals(&in)
		Expect(in.Config.OnApproval).To(BeNil())
	})
})
