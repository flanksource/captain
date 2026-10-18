package promptrun

import (
	"context"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = g.Describe("Caller-tool approval binding", func() {
	question := api.ApprovalRequest{Tool: "todo_ask", Kind: api.ApprovalKindQuestion,
		Questions: []api.TerminalQuestion{{Text: "Which survives?"}}}

	g.It("hands each caller-tool handler the run's approval callback through its context", func() {
		answers := map[string]any{"answers": map[string]any{"0": "the older one"}}
		var asked []api.ApprovalRequest
		in := Input{Config: api.Config{
			OnApproval: func(_ context.Context, req api.ApprovalRequest) (api.ApprovalDecision, error) {
				asked = append(asked, req)
				return api.ApprovalDecision{Allow: true, UpdatedInput: answers}, nil
			},
			Tools: []api.ToolDefinition{{Name: "todo_ask", Handler: func(ctx context.Context, _ map[string]any) (any, error) {
				approve, ok := api.ApprovalFuncFrom(ctx)
				Expect(ok).To(BeTrue())
				decision, err := approve(ctx, question)
				return decision.UpdatedInput, err
			}}},
		}}
		Expect(bindToolApprovals(&in)).To(Succeed())

		out, err := in.Config.Tools[0].Handler(context.Background(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal(answers))
		Expect(asked).To(Equal([]api.ApprovalRequest{question}))
	})

	g.It("leaves handlers without an approval callback when the run has none", func() {
		in := Input{Config: api.Config{Tools: []api.ToolDefinition{{Name: "todo_ask", Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			_, ok := api.ApprovalFuncFrom(ctx)
			return ok, nil
		}}}}}
		Expect(bindToolApprovals(&in)).To(Succeed())

		out, err := in.Config.Tools[0].Handler(context.Background(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(BeFalse())
	})
})
