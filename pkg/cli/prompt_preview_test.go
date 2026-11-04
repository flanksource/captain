package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prepared prompt preview", func() {
	It("returns the captured render without allowing presentation edits to mutate the admitted prompt", func() {
		prepared := &PreparedPrompt{rendered: PromptRenderResult{ID: "prompt-id", Name: "review", User: "Review captured files", OutputSchema: map[string]any{"type": "object"}}}
		view, err := prepared.Preview()
		Expect(err).NotTo(HaveOccurred())
		Expect(view.User).To(Equal("Review captured files"))
		Expect(view.ID).To(Equal("prompt-id"))
		view.OutputSchema["type"] = "string"
		Expect(prepared.rendered.OutputSchema["type"]).To(Equal("object"))
	})
})
