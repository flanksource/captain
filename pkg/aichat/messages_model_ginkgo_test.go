package aichat

import (
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = ginkgo.Describe("chatModel", func() {
	// The chat catalog (/api/chat/models) publishes ids as "provider/model",
	// and the browser sends that id back in ChatRequest.Model. Resolution must
	// yield the provider's backend rather than leaving it empty for the
	// configured default agent to claim.
	fallback := api.Model{Name: "gpt-5.6-luna", Backend: api.BackendCodexAgent}

	ginkgo.DescribeTable("resolves a catalog id to its concrete backend",
		func(id string, wantName string, wantBackend api.Backend) {
			resolved, err := chatModel(ChatRequest{Model: id}, fallback)
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved.Name).To(Equal(wantName))
			Expect(resolved.Backend).To(Equal(wantBackend))
		},
		ginkgo.Entry("anthropic api model", "anthropic/claude-opus-5", "claude-opus-5", api.BackendAnthropic),
		ginkgo.Entry("openai api model", "openai/gpt-5.6", "gpt-5.6", api.BackendOpenAI),
	)

	ginkgo.It("does not treat a catalog id and its own structured runtime as a conflict", func() {
		runtime := api.Model{Name: "claude-opus-5", Backend: api.BackendAnthropic}
		resolved, err := chatModel(ChatRequest{
			Model:   "anthropic/claude-opus-5",
			Runtime: &runtime,
		}, fallback)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Backend).To(Equal(api.BackendAnthropic))
	})

	ginkgo.It("still rejects a model that disagrees with the structured runtime", func() {
		runtime := api.Model{Name: "claude-opus-5", Backend: api.BackendAnthropic}
		_, err := chatModel(ChatRequest{
			Model:   "openai/gpt-5.6",
			Runtime: &runtime,
		}, fallback)
		Expect(err).To(MatchError(ContainSubstring("conflicts with structured runtime")))
	})
})
