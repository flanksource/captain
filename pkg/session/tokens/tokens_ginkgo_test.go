package tokens

import (
	"context"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"
)

func TestTokens(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Session Token Sizing Suite") }

var _ = Describe("Session token sizing", func() {
	var detail *session.Session
	BeforeEach(func() {
		detail = &session.Session{ID: "session-example", Revision: 3, Model: "claude-sonnet-4-6", ModelMode: api.ModeAgent,
			Usage: api.Usage{InputTokens: 100}, Cost: api.Cost{InputCost: 1},
			Messages: []session.Message{
				{ID: "user", Role: "user", Parts: []session.Part{{Type: session.PartText, Text: "abcdefgh"}}},
				{ID: "assistant", Role: "assistant", Parts: []session.Part{{Type: session.PartText, Text: "abcdefgh"}, {Type: session.PartReasoning, Text: "abcd"}}},
				{ID: "tool", Role: "assistant", Parts: []session.Part{{Type: session.PartTool, ToolName: "read", Input: json.RawMessage(`{"path":"example"}`), Output: json.RawMessage(`"abcdefgh"`), EstimatedCost: &session.ToolCostEstimate{Cost: api.Cost{InputTokens: 100, InputCost: 1}, SharedCalls: 1}}}},
			},
		}
	})
	It("uses canonical part IDs and separates input output and reasoning without changing totals", func() {
		result, err := Size(context.Background(), detail, Options{Method: "estimate"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(HaveLen(4))
		Expect(result.Rows[0].RowID).To(Equal("user-0"))
		Expect(result.Rows[0].Size.Usage).To(Equal(api.Usage{InputTokens: 2}))
		Expect(result.Rows[1].Size.Usage).To(Equal(api.Usage{OutputTokens: 2}))
		Expect(result.Rows[2].Size.Usage).To(Equal(api.Usage{ReasoningTokens: 1}))
		Expect(result.Rows[3].Attributed).To(BeTrue())
		Expect(result.Rows[3].Size.Usage.InputTokens).To(BeNumerically(">", 0))
		Expect(result.Rows[3].Size.Usage.OutputTokens).To(BeNumerically(">", 0))
		Expect(detail.Usage).To(Equal(api.Usage{InputTokens: 100}))
		Expect(detail.Cost.InputCost).To(Equal(float64(1)))
	})
	It("rejects unknown row IDs and stale revisions", func() {
		_, err := Size(context.Background(), detail, Options{RowIDs: []string{"other-0"}})
		Expect(err).To(MatchError(ContainSubstring("row")))
		revision := int64(2)
		_, err = Size(context.Background(), detail, Options{Revision: &revision})
		Expect(err).To(MatchError(ContainSubstring("revision")))
	})
	It("retains media rows as explicit partial results without invented counts", func() {
		detail.Messages = []session.Message{{ID: "file", Role: "user", Parts: []session.Part{{Type: session.PartFile, URL: "unavailable.png", MediaType: "image/png"}}}}
		result, err := Size(context.Background(), detail, Options{Method: "estimate"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows[0].Size.TotalTokens).To(BeZero())
		Expect(result.Rows[0].Size.Coverage.Partial).To(BeTrue())
	})
	It("prices each row using its own model provenance", func() {
		detail.Messages[1].Provenance = &session.Provenance{Model: "gpt-5"}
		result, err := Size(context.Background(), detail, Options{RowIDs: []string{"assistant-0"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows[0].Size.Model).To(Equal("gpt-5"))
	})
	It("keeps synthetic API error rows from preventing valid row estimates", func() {
		detail.Messages = append(detail.Messages, session.Message{ID: "failed", Role: "assistant", Provenance: &session.Provenance{APIErrorStatus: 429}})
		result, err := Size(context.Background(), detail, Options{RowIDs: []string{"user-0", "failed-err"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0].Size.TotalTokens).To(Equal(2))
		Expect(result.Rows[1].Error).To(ContainSubstring("no model content"))
	})
})
