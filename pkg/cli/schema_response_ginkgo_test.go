package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type schemaFailureProvider struct{ streamProviderStub }

func (p schemaFailureProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{Text: "# Findings\n\n- Define a baseline.", Usage: ai.Usage{OutputTokens: 30}, CostUSD: 0.75}, fmt.Errorf("%w: expected JSON", ai.ErrSchemaValidation)
}

var _ = Describe("Rejected schema response rendering", func() {
	DescribeTable("returns the rejected response and spend with the schema error", func(buffered bool) {
		provider := &schemaFailureProvider{streamProviderStub{events: []ai.Event{
			{Kind: ai.EventText, Text: "# Findings\n\n- Define a baseline.", Rejected: true},
			{Kind: ai.EventError, Error: "schema validation failed: expected JSON", Usage: &ai.Usage{OutputTokens: 30}, CostUSD: 0.75},
		}}}
		var diagnostic bytes.Buffer
		ctx := context.WithValue(context.Background(), promptRendererKey{}, newEventRenderer(&diagnostic, false))
		var output any
		var err error
		if buffered {
			output, err = runBuffered(ctx, provider, ai.Request{})
		} else {
			output, err = runStreaming(ctx, provider, ai.Request{})
		}
		Expect(err).To(MatchError(ContainSubstring(ai.ErrSchemaValidation.Error())))
		Expect(output).To(BeAssignableToTypeOf(AIPromptResult{}))
		result := output.(AIPromptResult)
		Expect(result.Text).To(Equal("# Findings\n\n- Define a baseline."))
		Expect(result.Output).To(Equal(30))
		Expect(result.CostUSD).To(Equal(0.75))
	}, Entry("buffered", true), Entry("streamed", false))

	It("renders the complete Markdown before the schema error", func() {
		response := "I recorded 11 findings in `report.mdx`.\n\n- **Goals:** " + strings.Repeat("baseline ", 40) + "\n- **Owners:** name the accountable team."
		var output bytes.Buffer
		renderer := newEventRenderer(&output, false)
		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: response, Rejected: true})
		renderer.Handle(0, ai.Event{Kind: ai.EventError, Error: "schema validation failed: expected JSON"})
		Expect(renderer.Flush()).To(Succeed())
		Expect(output.String()).To(And(ContainSubstring("name the accountable team"), ContainSubstring("expected JSON")))
		Expect(strings.Index(output.String(), "name the accountable team")).To(BeNumerically("<", strings.Index(output.String(), "expected JSON")))
		Expect(strings.Count(output.String(), "I recorded 11 findings")).To(Equal(1))
		Expect(strings.Count(output.String(), "baseline")).To(Equal(40))
	})
})
