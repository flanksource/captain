package ai_test

import (
	"github.com/flanksource/captain/pkg/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Schema response format", func() {
	DescribeTable("distinguishes Markdown from JSON before rendering", func(body string, markdown bool) {
		Expect(ai.IsMarkdown(body)).To(Equal(markdown))
	}, Entry("plain prose", "I recorded findings.", true),
		Entry("list", "- first\n- second", true),
		Entry("ordered list", "1. first\n2. second", true),
		Entry("heading", "# Findings", true),
		Entry("fenced JSON", "```json\n{\"ok\":true}\n```", true),
		Entry("JSON with Markdown text", `{"message":"# Review\n- **Finding**"}`, false),
		Entry("malformed JSON", `{"message":`, false),
		Entry("empty", "  ", false))
})
