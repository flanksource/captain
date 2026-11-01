package agent

import (
	"context"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Runner schema rejection", func() {
	It("retains the rejected answer and spend when generation fails", func() {
		response := "# Findings\n\n- Define a baseline."
		provider := &fakeProvider{events: func(int) []ai.Event {
			return []ai.Event{
				{Kind: ai.EventText, Text: response, Rejected: true},
				{Kind: ai.EventError, Error: ai.ErrSchemaValidation.Error(), Usage: &ai.Usage{OutputTokens: 30}, CostUSD: 0.75},
			}
		}}
		runner := Runner[string]{Provider: provider, Request: ai.Request{Prompt: api.Prompt{User: "Review the report."}}}
		result, err := runner.Run(context.Background())
		Expect(err).To(MatchError(ContainSubstring(ai.ErrSchemaValidation.Error())))
		Expect(result.Response.Text).To(Equal(response))
		Expect(result.Response.Usage.OutputTokens).To(Equal(30))
		Expect(result.Response.CostUSD).To(Equal(0.75))
	})
})
