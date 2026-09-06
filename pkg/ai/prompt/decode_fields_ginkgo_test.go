package prompt

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Prompt declaration field policy", func() {
	DescribeTable("rejects unknown native declarations before rendering or execution", func(frontmatter string) {
		_, err := Parse("---\n" + frontmatter + "\n---\nReview the change\n")
		Expect(err).To(MatchError(ContainSubstring("unexpected")))
	},
		Entry("top level", "unexpected: false"),
		Entry("budget", "budget: {unexpected: 0}"),
		Entry("fallback", "fallbacks: [{model: sonnet, unexpected: false}]"),
		Entry("runtime", "runtimes: [{model: sonnet, unexpected: false}, api:sol]"),
		Entry("runtime fallback", "runtimes: [{model: sonnet, fallbacks: [{model: sol, unexpected: 0}]}, api:sol]"),
	)

	It("retains zero declarations after strict field inspection", func() {
		doc, err := Parse("---\nmodel: sonnet\nnoCache: false\nbudget: {cost: 0}\n---\nReview\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(doc.Spec.Fields()).To(HaveKey("/noCache"))
		Expect(doc.Spec.Fields()).To(HaveKey("/budget/cost"))
	})
})
