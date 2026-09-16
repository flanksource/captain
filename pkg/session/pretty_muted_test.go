package session

import (
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPrettyMuted(t *testing.T) {
	RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Session Pretty Muted Suite")
}

var _ = ginkgo.Describe("session Pretty muted styles", func() {
	ginkgo.It("separates current context occupancy from cumulative token traffic", func() {
		session := &Session{
			Context: &Context{UsedTokens: 88_818, WindowTokens: 258_400, FreePercent: 66},
			Usage: api.Usage{
				InputTokens: 86_514, OutputTokens: 2_223, ReasoningTokens: 2_971, CacheReadTokens: 855_296,
			},
		}

		rendered := session.Pretty()

		Expect(rendered.String()).To(ContainSubstring("Context: 88.8K / 258.4K (34% used, 66% free)"))
		Expect(rendered.String()).To(ContainSubstring("Tokens: 947.0K cumulative"))
		Expect(rendered.Markdown()).To(ContainSubstring("88.8K / 258.4K"))
		Expect((&Session{Usage: session.Usage}).Pretty().String()).NotTo(ContainSubstring("Context:"))
	})

	ginkgo.It("renders schema-constrained output as formatted JSON", func() {
		session := &Session{StructuredOutput: map[string]any{
			"scorecards": []any{map[string]any{"dimension": "answer-relevancy", "score": float64(5)}},
		}}

		rendered := session.Pretty().String()

		Expect(rendered).To(ContainSubstring("Structured Output"))
		Expect(rendered).To(ContainSubstring(`"dimension": "answer-relevancy"`))
	})

	ginkgo.It("renders summary values and assistant payloads without low-contrast palette classes", func() {
		session := &Session{
			ID:      "session-id",
			Project: "tenant-x",
			Messages: []Message{{
				Role:  "assistant",
				Parts: []Part{{Type: PartText, Text: "assistant payload"}},
			}},
		}

		rendered := session.Pretty()
		Expect(rendered.HTML()).NotTo(MatchRegexp(`text-gray-(600|700)`))
		Expect(rendered.HTML()).To(ContainSubstring("text-muted"))
		Expect(rendered.String()).To(ContainSubstring("tenant-x"))
		Expect(rendered.String()).To(ContainSubstring("assistant payload"))
	})
})
