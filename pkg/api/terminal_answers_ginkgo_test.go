package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("AnswersForQuestions", func() {
	scope := api.TerminalQuestion{ID: "scope", Text: "How far?", Options: []string{"Phase 1", "All phases"}}
	surface := api.TerminalQuestion{Text: "Which surface?", Options: []string{"CLI", "HTTP"}}
	areas := api.TerminalQuestion{Text: "Which areas?", Options: []string{"API", "UI"}, MultiSelect: true}

	DescribeTable("resolves answers keyed by any of the three question identities",
		func(questions []api.TerminalQuestion, submitted map[string]any, expected [][]string) {
			answers, err := api.AnswersForQuestions(questions, submitted)
			Expect(err).ToNot(HaveOccurred())
			choices := make([][]string, 0, len(answers))
			for i, answer := range answers {
				Expect(answer.Index).To(Equal(i + 1))
				Expect(answer.Question.Text).To(Equal(questions[i].Text))
				choices = append(choices, answer.Choices)
			}
			Expect(choices).To(Equal(expected))
		},
		Entry("by id", []api.TerminalQuestion{scope}, map[string]any{"scope": "Phase 1"}, [][]string{{"Phase 1"}}),
		Entry("by 1-based index", []api.TerminalQuestion{surface, areas},
			map[string]any{"1": "CLI", "2": []any{"API"}}, [][]string{{"CLI"}, {"API"}}),
		Entry("by exact text", []api.TerminalQuestion{surface},
			map[string]any{"Which surface?": "HTTP"}, [][]string{{"HTTP"}}),
		Entry("trims each choice", []api.TerminalQuestion{areas},
			map[string]any{"1": []any{" API ", "UI"}}, [][]string{{"API", "UI"}}),
		Entry("prefers the id over the index when both would match",
			[]api.TerminalQuestion{{ID: "2", Text: "First?"}, {Text: "Second?"}},
			map[string]any{"2": "by id", "Second?": "by text"}, [][]string{{"by id"}, {"by text"}}),
	)

	DescribeTable("rejects answers the host would drop",
		func(questions []api.TerminalQuestion, submitted any) {
			_, err := api.AnswersForQuestions(questions, submitted)
			Expect(err).To(HaveOccurred())
		},
		Entry("not an object", []api.TerminalQuestion{surface}, "CLI"),
		Entry("missing question", []api.TerminalQuestion{surface, areas}, map[string]any{"1": "CLI"}),
		Entry("unmatched key", []api.TerminalQuestion{surface}, map[string]any{"1": "CLI", "9": "extra"}),
		Entry("blank choice", []api.TerminalQuestion{surface}, map[string]any{"1": "  "}),
		Entry("empty list", []api.TerminalQuestion{areas}, map[string]any{"1": []any{}}),
		Entry("non-text choice", []api.TerminalQuestion{areas}, map[string]any{"1": []any{7}}),
		Entry("list for a single-select question", []api.TerminalQuestion{surface}, map[string]any{"1": []any{"CLI"}}),
		Entry("no questions", nil, map[string]any{"1": "CLI"}),
	)

	It("accepts a []string answer as well as a decoded []any", func() {
		answers, err := api.AnswersForQuestions([]api.TerminalQuestion{areas}, map[string]any{"1": []string{"API", "UI"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(answers[0].Choices).To(Equal([]string{"API", "UI"}))
	})
})
