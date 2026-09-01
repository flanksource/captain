package ai

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("runtime model selection", func() {
	It("derives the provider from a bare model and takes that provider's default mode", func() {
		model, err := Resolve(api.Model{Name: "gemini-3.5-flash"})

		Expect(err).NotTo(HaveOccurred())
		Expect(model.Name).To(Equal("gemini-3.5-flash"))
		Expect(model.Provider).To(Equal(api.Google))
		Expect(model.Mode).To(Equal(api.Google.DefaultMode))
	})

	It("resolves each item of a multi-model list independently", func() {
		models, err := ResolveMulti(
			[]string{"gemini-3.5-flash,gpt-5.5"},
			api.Model{Name: "opus", Mode: api.ModeAgent},
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(models).To(HaveLen(2))
		Expect(models[0].Name).To(Equal("gemini-3.5-flash"))
		Expect(models[0].Provider).To(Equal(api.Google))
		Expect(models[1].Name).To(Equal("gpt-5.5"))
		Expect(models[1].Provider).To(Equal(api.OpenAI))
	})

	It("rejects a model whose provider does not serve the selected mode", func() {
		_, err := Resolve(api.Model{
			Name: "gemini-3.5-flash",
			Mode: api.ModeAgent,
		})

		Expect(err).To(MatchError(ContainSubstring("gemini")))
		Expect(err).To(MatchError(ContainSubstring("agent")))
	})

	It("requires a claimable family: a mode alone does not name a provider", func() {
		_, err := Resolve(api.Model{
			Name: "provider-future-model",
			Mode: api.ModeAPI,
		})

		Expect(err).To(MatchError(ContainSubstring("provider-future-model")))
	})
})
