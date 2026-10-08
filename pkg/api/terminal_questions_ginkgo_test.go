package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("terminal questions from a native tool input", func() {
	DescribeTable("normalizes every question shape an agent emits",
		func(input map[string]any, want []api.TerminalQuestion) {
			questions, err := api.TerminalQuestionsFromInput(input)

			Expect(err).NotTo(HaveOccurred())
			Expect(questions).To(Equal(want))
		},
		Entry("AskUserQuestion with described and plain options",
			map[string]any{"questions": []any{map[string]any{
				"question": "Which database?", "header": "Storage",
				"options": []any{map[string]any{"label": "PostgreSQL", "description": "Production"}, "SQLite"},
			}}},
			[]api.TerminalQuestion{{
				Text: "Which database?", Context: "Storage", Options: []string{"PostgreSQL", "SQLite"},
				OptionDescriptions: map[string]string{"PostgreSQL": "Production"},
			}}),
		Entry("AskUserQuestion that lets the person pick several options",
			map[string]any{"questions": []any{map[string]any{
				"question": "Where should it surface?", "multiSelect": true,
				"options": []any{map[string]any{"label": "Storybook"}, map[string]any{"label": "Demo"}},
			}}},
			[]api.TerminalQuestion{{Text: "Where should it surface?", MultiSelect: true, Options: []string{"Storybook", "Demo"}}}),
		Entry("an envelope question in snake case that lets the person pick several options",
			map[string]any{"questions": []any{map[string]any{"text": "Which checks?", "multi_select": true, "options": []any{"lint", "test"}}}},
			[]api.TerminalQuestion{{Text: "Which checks?", MultiSelect: true, Options: []string{"lint", "test"}}}),
		Entry("text padded with whitespace, as a model often writes it",
			map[string]any{"questions": []any{map[string]any{"text": "Which work?\n", "context": " Disjoint ", "options": []any{map[string]any{"label": " Plan ", "description": " Ship it \n"}}}}},
			[]api.TerminalQuestion{{Text: "Which work?", Context: "Disjoint", Options: []string{"Plan"}, OptionDescriptions: map[string]string{"Plan": "Ship it"}}}),
		Entry("a single question at the top level",
			map[string]any{"prompt": "Continue?"},
			[]api.TerminalQuestion{{Text: "Continue?"}}),
		Entry("a result envelope's text/context/options questions",
			map[string]any{"questions": []any{
				map[string]any{"text": "Which work?", "context": "Disjoint scopes", "options": []any{"Plan", "Todo"}},
				map[string]any{"text": "Which layout?"},
			}},
			[]api.TerminalQuestion{
				{Text: "Which work?", Context: "Disjoint scopes", Options: []string{"Plan", "Todo"}},
				{Text: "Which layout?"},
			}),
	)

	DescribeTable("fails on input that cannot be asked",
		func(input map[string]any, wantErr string) {
			questions, err := api.TerminalQuestionsFromInput(input)

			Expect(questions).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring(wantErr)))
		},
		Entry("questions that are not an array", map[string]any{"questions": "Which?"}, "questions must be an array"),
		Entry("an empty question list", map[string]any{"questions": []any{}}, "terminal questions are required"),
		Entry("a question without text", map[string]any{"questions": []any{map[string]any{"context": "why"}}}, "question 1: one of question, prompt, text is required"),
		Entry("an option that is neither string nor object",
			map[string]any{"questions": []any{map[string]any{"text": "Continue?", "options": []any{42}}}}, "question 1: option 1"),
		Entry("a multi-select flag that is not a boolean",
			map[string]any{"questions": []any{map[string]any{"text": "Which checks?", "multiSelect": "yes"}}}, "question 1: multiSelect must be a boolean"),
	)
})
