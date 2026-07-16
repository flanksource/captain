package ai

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("runtime model selection", func() {
	It("infers a concrete backend for a bare model", func() {
		model, err := ResolveModelSelectors(api.Model{Name: "gemini-3.5-flash"})

		Expect(err).NotTo(HaveOccurred())
		Expect(model.Name).To(Equal("gemini-3.5-flash"))
		Expect(model.Backend).To(Equal(api.BackendGemini))
	})

	It("infers each bare multi-model runtime independently", func() {
		models, err := ResolveRuntimeSelectors(
			[]string{"gemini-3.5-flash,gpt-5.5"},
			api.Model{Name: "opus", Backend: api.BackendClaudeAgent},
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(models).To(Equal([]api.Model{
			{Name: "gemini-3.5-flash", Backend: api.BackendGemini},
			{Name: "gpt-5.5", Backend: api.BackendOpenAI},
		}))
	})

	It("rejects a known model forced through another family", func() {
		_, err := ResolveModelSelectors(api.Model{
			Name:    "gemini-3.5-flash",
			Backend: api.BackendClaudeAgent,
		})

		Expect(err).To(MatchError(ContainSubstring("gemini")))
		Expect(err).To(MatchError(ContainSubstring("claude-agent")))
	})

	It("allows an unknown provider model when its backend is explicit", func() {
		model, err := ResolveModelSelectors(api.Model{
			Name:    "provider-future-model",
			Backend: api.BackendGemini,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(model.Name).To(Equal("provider-future-model"))
		Expect(model.Backend).To(Equal(api.BackendGemini))
	})

	It("requires a backend for an unknown bare model", func() {
		_, err := ResolveModelSelectors(api.Model{Name: "provider-future-model"})

		Expect(err).To(MatchError(ContainSubstring("pass an explicit backend")))
	})
})
