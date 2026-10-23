package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prompt run --vars schema", func() {
	It("is the prompt's input schema with its input defaults as property defaults", func() {
		raw, err := promptInputSchema(PromptDetail{
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"diff"},
				"properties": map[string]any{
					"diff":   map[string]any{"type": "string"},
					"strict": map[string]any{"type": "boolean"},
					"length": map[string]any{"type": "integer", "default": 5},
				},
			},
			InputDefault: map[string]any{"strict": true, "length": 3},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(MatchJSON(`{
			"type": "object",
			"required": ["diff"],
			"properties": {
				"diff": {"type": "string"},
				"strict": {"type": "boolean", "default": true},
				"length": {"type": "integer", "default": 5}
			}
		}`), "a property's own default wins over the input default")
	})

	It("is nothing for a prompt without inputs", func() {
		raw, err := promptInputSchema(PromptDetail{})
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(BeNil())
	})
})

var _ = DescribeTable("firstClause keeps a description to its first sentence or clause",
	func(description, want string) { Expect(firstClause(description)).To(Equal(want)) },
	Entry("sentence", "Denies edit and write. It sets no read policy.", "Denies edit and write"),
	Entry("clause before a colon", "Plan mode: the agent reads but does not act.", "Plan mode"),
	Entry("single sentence", "Return structured lint violations.", "Return structured lint violations"),
	Entry("empty", "", ""),
)
