package aichat

import (
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = ginkgo.Describe("chatModel composition", func() {
	// The chat catalog (/api/chat/models) publishes ids as "provider/model", and
	// the browser sends that id back in ChatRequest.Model. Resolution must yield
	// the provider the id names rather than leaving it empty for the configured
	// default to claim — that gap is how an agent-backed session came back as api.
	// A catalog id carries no mode, so the authored profile mode survives.
	resolve := func(request ChatRequest) (api.Model, error) {
		composed, err := api.ComposeSpecLayers(api.ResolveSpecOptions{Layers: []api.SpecLayer{{Name: "application", Scope: api.SpecLayerGlobal,
			Spec: api.Spec{Model: api.Model{Name: "gpt-5.6-luna", Mode: api.ModeAPI}},
		}}})
		Expect(err).NotTo(HaveOccurred())
		request.Messages = []UIMessage{{Role: "user", Parts: []UIPart{{Type: "text", Text: "Hello"}}}}
		resolved, err := requestSpec(request, RuntimeProfile{Composed: composed}, nil)
		return resolved.Spec.Model, err
	}

	ginkgo.DescribeTable("resolves a catalog id to its concrete runtime",
		func(id string, wantName string, wantProvider *api.ModelProvider, wantMode api.RuntimeMode) {
			resolved, err := resolve(ChatRequest{Model: id})
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved.Name).To(Equal(wantName))
			Expect(resolved.Provider).To(Equal(wantProvider))
			Expect(resolved.Mode).To(Equal(wantMode))
		},
		ginkgo.Entry("anthropic catalog id", "anthropic/claude-opus-5", "claude-opus-5", api.Anthropic, api.ModeAPI),
		ginkgo.Entry("openai catalog id", "openai/gpt-5.6-sol", "gpt-5.6-sol", api.OpenAI, api.ModeAPI),
	)

	ginkgo.It("does not treat a catalog id and its own structured runtime as a conflict", func() {
		runtime := api.Model{Name: "claude-opus-5", Mode: api.ModeAPI}
		resolved, err := resolve(ChatRequest{
			Model:   "anthropic/claude-opus-5",
			Runtime: &runtime,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Provider).To(Equal(api.Anthropic))
		Expect(resolved.Mode).To(Equal(api.ModeAPI))
	})

	ginkgo.It("still rejects a model that disagrees with the structured runtime", func() {
		runtime := api.Model{Name: "claude-opus-5", Mode: api.ModeAPI}
		_, err := resolve(ChatRequest{
			Model:   "openai/gpt-5.6-sol",
			Runtime: &runtime,
		})
		Expect(err).To(MatchError(ContainSubstring("conflicts with structured runtime")))
	})
})
