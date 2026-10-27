package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSchemaRepair(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Schema Repair Suite")
}

const rejectedMarkdown = "I recorded 11 findings in `report.mdx`.\n\n- **Goals:** define a baseline.\n- **Owners:** name the team."

type repairScript struct {
	requests    []ai.Request
	replies     []string
	repairError error
}

func (p *repairScript) GetModel() string       { return "test-model" }
func (p *repairScript) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Anthropic, ai.ModeAPI) }
func (p *repairScript) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return nil, fmt.Errorf("stream only")
}
func (p *repairScript) ExecuteStream(_ context.Context, req ai.Request) (<-chan ai.Event, error) {
	if len(p.requests) > 0 && p.repairError != nil {
		return nil, p.repairError
	}
	body := p.replies[min(len(p.requests), len(p.replies)-1)]
	p.requests = append(p.requests, req)
	out := make(chan ai.Event, 1)
	if json.Valid([]byte(body)) {
		out <- ai.Event{Kind: ai.EventResult, Success: true, StructuredData: json.RawMessage(body), Usage: &ai.Usage{OutputTokens: 10}, CostUSD: streamCostUSD}
	} else {
		out <- ai.Event{Kind: ai.EventError, Text: body, Error: ai.ErrSchemaValidation.Error() + ": expected JSON", Usage: &ai.Usage{OutputTokens: 10}, CostUSD: streamCostUSD}
	}
	close(out)
	return out, nil
}

var _ = Describe("Schema repair responses", func() {
	It("preserves the rejected turn's spend when a repair cannot start", func() {
		inner := &repairScript{replies: []string{rejectedMarkdown}, repairError: fmt.Errorf("provider unavailable")}
		provider, err := WithSchemaValidation()(inner)
		Expect(err).NotTo(HaveOccurred())
		events, err := provider.(ai.StreamingProvider).ExecuteStream(context.Background(), capRequest(api.SchemaStrictnessRepair))
		Expect(err).NotTo(HaveOccurred())
		var terminal ai.Event
		for event := range events {
			terminal = event
		}
		Expect(terminal.Error).To(ContainSubstring("provider unavailable"))
		Expect(terminal.CostUSD).To(Equal(streamCostUSD))
		Expect(terminal.Usage).To(Equal(&ai.Usage{OutputTokens: 10}))
	})

	DescribeTable("shows rejected Markdown before repair or terminal failure", func(policy string, replies []string, calls int, success bool) {
		inner := &repairScript{replies: replies}
		provider, err := WithSchemaValidation()(inner)
		Expect(err).NotTo(HaveOccurred())
		events, err := provider.(ai.StreamingProvider).ExecuteStream(context.Background(), capRequest(api.SchemaStrictness(policy)))
		Expect(err).NotTo(HaveOccurred())
		var received []ai.Event
		for event := range events {
			received = append(received, event)
		}
		Expect(inner.requests).To(HaveLen(calls))
		rejectedCount := calls
		if success {
			rejectedCount--
		}
		Expect(eventsOfKind(received, ai.EventText)).To(HaveLen(rejectedCount))
		Expect(received[0].Kind).To(Equal(ai.EventText))
		Expect(received[0].Text).To(Equal(rejectedMarkdown))
		if success {
			Expect(eventsOfKind(received, ai.EventError)).To(BeEmpty())
			Expect(received[len(received)-1].StructuredData).To(MatchJSON(validJSON))
		} else {
			Expect(eventsOfKind(received, ai.EventError)).To(HaveLen(1))
		}
		Expect(received[len(received)-1].Usage.OutputTokens).To(Equal(calls * 10))
	}, Entry("repair", "repair", []string{rejectedMarkdown, validJSON}, 2, true),
		Entry("retry alias", "retry", []string{rejectedMarkdown, validJSON}, 2, true),
		Entry("error", "error", []string{rejectedMarkdown}, 1, false),
		Entry("exhausted", "repair", []string{rejectedMarkdown}, 3, false))

	It("repairs malformed JSON and preserves the final rejected buffered response", func() {
		inner := &scriptedProvider{responses: []string{rejectedMarkdown}}
		provider, err := WithSchemaValidation()(inner)
		Expect(err).NotTo(HaveOccurred())
		response, err := provider.Execute(context.Background(), capRequest(api.SchemaStrictness("repair")))
		Expect(err).To(MatchError(ContainSubstring(ai.ErrSchemaValidation.Error())))
		Expect(inner.calls).To(Equal(3))
		Expect(response).NotTo(BeNil())
		Expect(response.Text).To(Equal(rejectedMarkdown))
	})

	It("appends repair feedback to canonical messages and disables caller tools", func() {
		provider := &validatingProvider{provider: &scriptedProvider{}}
		parent := capRequest(api.SchemaStrictnessRepair)
		parent.Messages = []api.Message{{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Review the report."}}}}
		parent.ToolApproval = &api.ToolApprovalResume{}
		repaired, _, _, err := provider.repairRequest(parent, json.RawMessage(capSchema), "expected JSON", &ai.Response{Text: rejectedMarkdown}, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(repaired.Messages).To(HaveLen(2))
		Expect(repaired.Messages[1].Parts[0].Text).To(ContainSubstring(rejectedMarkdown))
		Expect(parent.Messages).To(HaveLen(1))
		Expect(repaired.ToolApproval).To(BeNil())
		Expect(repaired.ToolPolicy.Validate()).To(Succeed())
		policy, matched := repaired.ToolPolicy.Resolve(api.ToolInfo{Name: "report_violation"})
		Expect(matched).To(BeTrue())
		Expect(policy).To(Equal(api.ToolPolicyDeny))
	})
})
