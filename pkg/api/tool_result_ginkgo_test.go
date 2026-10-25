package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("ToolOutput", func() {
	type page struct {
		Data []map[string]any `json:"data"`
		Page map[string]int   `json:"page"`
	}
	structured := page{Data: []map[string]any{{"id": "j-1"}}, Page: map[string]int{"limit": 25, "offset": 0}}

	It("sends a StructuredToolResult's content to the model and its structured value to the UI", func() {
		output, err := api.ToolOutput(api.StructuredToolResult{Content: "| id |\n| j-1 |", Structured: structured})
		Expect(err).NotTo(HaveOccurred())
		Expect(output.Text).To(Equal("| id |\n| j-1 |"))
		Expect(output.Display).To(MatchJSON(`{"data":[{"id":"j-1"}],"page":{"limit":25,"offset":0}}`))
	})

	It("passes a plain string through as model text with no separate display", func() {
		output, err := api.ToolOutput("split into 3 lines")
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(api.ToolOutputParts{Text: "split into 3 lines"}))
	})

	It("encodes any other value as JSON model text with no separate display", func() {
		output, err := api.ToolOutput(structured)
		Expect(err).NotTo(HaveOccurred())
		Expect(output.Text).To(MatchJSON(`{"data":[{"id":"j-1"}],"page":{"limit":25,"offset":0}}`))
		Expect(output.Display).To(BeNil())
	})

	It("rejects a StructuredToolResult with no model content, since the model would receive nothing", func() {
		_, err := api.ToolOutput(api.StructuredToolResult{Structured: structured})
		Expect(err).To(MatchError(ContainSubstring("content")))
	})

	It("reports a structured value that cannot be encoded instead of dropping it", func() {
		_, err := api.ToolOutput(api.StructuredToolResult{Content: "rows", Structured: map[string]any{"bad": func() {}}})
		Expect(err).To(HaveOccurred())
	})

	It("gives the model only a StructuredToolResult's content and any other value unchanged", func() {
		Expect(api.ModelValue(api.StructuredToolResult{Content: "rows", Structured: structured})).To(Equal("rows"))
		Expect(api.ModelValue(structured)).To(Equal(structured))
	})

	It("treats a nil result as empty model text", func() {
		output, err := api.ToolOutput(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal(api.ToolOutputParts{}))
	})
})
