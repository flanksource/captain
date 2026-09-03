package agent

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// countingVerifier votes once and records that it was asked.
type countingVerifier struct {
	name  string
	calls *int
	valid bool
}

func (v *countingVerifier) Name() string { return v.name }
func (v *countingVerifier) Verify(hc *HookContext) (VerifyResult, error) {
	*v.calls++
	report := api.NewNodeReport(api.VerifyKindFunc, v.name, api.VerifyNode{
		Name: v.name, Passed: v.valid, Failed: !v.valid,
	})
	report.Iteration = hc.Iteration + 1
	return VerifyResult{Valid: v.valid, Report: &report, Iteration: report.Iteration}, nil
}

var _ = Describe("Runner: what makes a run verify-only", func() {
	// The runner used to decide with its own test — "is the user prompt blank?" —
	// while every caller decided with Spec.IsVerifyOnly. The two disagreed on a
	// request carrying attachments or a message history and no user text: the
	// caller built a provider for a generating run, the runner skipped generation,
	// no Verify hook voted, and the run reported a pass having done nothing.
	It("takes the decision from the request, not from the prompt body", func() {
		calls := 0
		provider := &fakeProvider{events: func(int) []ai.Event {
			return []ai.Event{{Kind: ai.EventResult, Success: true}}
		}}
		req := ai.Request{Workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}}
		Expect(req.IsVerifyOnly()).To(BeTrue())

		r := &Runner[string]{Provider: provider, Request: req, Hooks: []any{&countingVerifier{name: "check", calls: &calls, valid: true}}}
		res, err := r.Run(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(provider.calls).To(BeZero(), "a verify-only run never generates")
		Expect(calls).To(Equal(1))
		Expect(res.Loop).To(BeNil())
	})

	DescribeTable("refuses a request that neither generates nor verifies",
		func(mutate func(*ai.Request)) {
			req := ai.Request{}
			mutate(&req)
			provider := &fakeProvider{events: func(int) []ai.Event { return nil }}

			_, err := (&Runner[string]{Provider: provider, Request: req}).Run(context.Background())
			Expect(err).To(MatchError(ContainSubstring("workflow.verify")))
			Expect(provider.calls).To(BeZero(), "the refusal comes before the first model call")
		},
		Entry("nothing at all", func(*ai.Request) {}),
		Entry("attachments but no prompt and no verification", func(r *ai.Request) {
			r.Prompt.Attachments = []api.AttachmentRef{{Path: "invoice.pdf"}}
		}),
		Entry("a message history but no prompt and no verification", func(r *ai.Request) {
			r.Messages = []api.Message{{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "hi"}}}}
		}),
	)

	It("still generates for a prompt whose body is only whitespace-free text", func() {
		provider := &fakeProvider{events: func(int) []ai.Event {
			return []ai.Event{{Kind: ai.EventResult, Success: true}}
		}}
		r := &Runner[string]{Provider: provider, Request: ai.Request{Prompt: api.Prompt{User: "fix it"}}}
		_, err := r.Run(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(provider.calls).To(Equal(1))
	})
})
