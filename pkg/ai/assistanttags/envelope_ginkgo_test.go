package assistanttags_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/assistanttags"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("ParseEnvelope", func() {
	DescribeTable("reads a result envelope with the questions it asks",
		func(text string, want assistanttags.Envelope) {
			envelope, ok, err := assistanttags.ParseEnvelope(text)

			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(envelope).To(Equal(want))
		},
		Entry("an ask with questions and options",
			`{"summary":"Blocked on scope.","endStatus":"ask","questions":[{"text":"Which work?","context":"Disjoint","options":["Plan","Todo"]},{"text":"Which layout?"}]}`,
			assistanttags.Envelope{Summary: "Blocked on scope.", EndStatus: "ask", Questions: []api.TerminalQuestion{
				{Text: "Which work?", Context: "Disjoint", Options: []string{"Plan", "Todo"}},
				{Text: "Which layout?"},
			}}),
		Entry("a completed plan envelope whose questions list is empty",
			`{"endStatus":"completed","plan":{"status":"new"},"questions":[],"summary":"Authored a plan.\n\n"}`,
			assistanttags.Envelope{Summary: "Authored a plan.", EndStatus: "completed"}),
		Entry("an ask that asked nothing",
			`{"summary":"Stuck.","endStatus":"ask"}`,
			assistanttags.Envelope{Summary: "Stuck.", EndStatus: "ask"}),
		Entry("a completed envelope whose leftover questions are malformed",
			`{"summary":"Done.","endStatus":"completed","questions":[{"context":"no text"}]}`,
			assistanttags.Envelope{Summary: "Done.", EndStatus: "completed"}),
	)

	DescribeTable("does not recognize text that is not an envelope",
		func(text string) {
			envelope, ok, err := assistanttags.ParseEnvelope(text)

			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
			Expect(envelope).To(BeZero())
		},
		Entry("ordinary prose", "Here is my plan: first do X, then Y."),
		Entry("malformed JSON", `{"summary":"x","endStatus":"ask",`),
		Entry("an unknown endStatus", `{"summary":"x","endStatus":"in_progress"}`),
		Entry("a blank summary", `{"summary":"  ","endStatus":"ask","questions":[{"text":"Which?"}]}`),
	)

	It("fails on an envelope whose questions cannot be asked, while its summary still reads", func() {
		text := `{"summary":"Blocked.","endStatus":"ask","questions":[{"context":"no text"}]}`

		_, ok, err := assistanttags.ParseEnvelope(text)

		Expect(ok).To(BeTrue())
		Expect(err).To(MatchError(ContainSubstring("envelope questions: question 1")))
		summary, summaryOK := assistanttags.EnvelopeSummary(text)
		Expect(summaryOK).To(BeTrue())
		Expect(summary).To(Equal("Blocked."))
	})
})
