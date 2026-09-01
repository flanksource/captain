package ai_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// resolved fills a hand-built model's provider and mode. Attachment validation
// reads the recorded runtime rather than re-deriving one, so an unresolved model
// is a caller bug it reports rather than silently guesses past.
func resolved(model api.Model) api.Model {
	out, err := api.ResolveModel(model)
	Expect(err).NotTo(HaveOccurred())
	return out
}

var _ = Describe("Attachment capabilities", func() {
	It("clamps catalog modalities to what the runtime can execute", func() {
		DeferCleanup(ai.ResetModelCatalog)
		model := ai.Model{
			ID:              "openai/vision-test",
			Provider:        ai.OpenAI,
			Mode:            ai.ModeAPI,
			InputMediaTypes: []string{"image/*", "audio/*"},
		}
		Expect(ai.SetModelCatalog([]ai.Model{model})).To(Succeed())
		info := ai.CatalogInfo([]string{"openai"})
		Expect(info).To(HaveLen(1))
		Expect(info[0].InputMediaTypes).To(Equal([]string{"image/*"}))
	})

	It("fails the entire multi-model request when one runtime is incompatible", func() {
		models := []api.Model{
			resolved(api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent}),
			resolved(api.Model{Name: "gemini-3.1-pro", Mode: api.ModeCLI}),
		}
		refs := []api.AttachmentRef{{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}}

		err := ai.ValidateAttachmentCompatibility(models, refs)
		Expect(err).To(MatchError(ContainSubstring("cli:gemini-3.1-pro-preview does not accept image/png attachments")))
	})

	It("rejects image formats the Claude Agent SDK cannot encode", func() {
		err := ai.ValidateAttachmentCompatibility(
			[]api.Model{resolved(api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent})},
			[]api.AttachmentRef{{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/svg+xml"}},
		)
		Expect(err).To(MatchError(ContainSubstring("does not accept image/svg+xml attachments")))
	})

	It("expands a model image wildcard to the Claude adapter formats", func() {
		model := resolved(api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent})
		ref := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}
		Expect(ai.ValidateAttachmentCompatibility([]api.Model{model}, []api.AttachmentRef{ref})).To(Succeed())
	})

	DescribeTable("publishes the adapter capability matrix per provider×mode cell",
		func(provider *api.ModelProvider, mode api.RuntimeMode, expected []string) {
			Expect(ai.AdapterInputMediaTypes(provider, mode)).To(Equal(expected))
		},
		Entry("Gemini API", api.Google, api.ModeAPI, []string{"image/*", "audio/*", "video/*", "application/pdf"}),
		Entry("Anthropic API", api.Anthropic, api.ModeAPI, []string{"image/*"}),
		Entry("OpenAI API", api.OpenAI, api.ModeAPI, []string{"image/*"}),
		Entry("DeepSeek API", api.DeepSeek, api.ModeAPI, []string{}),
		Entry("Codex agent", api.OpenAI, api.ModeAgent, []string{"image/*"}),
		Entry("Codex CLI", api.OpenAI, api.ModeCLI, []string{"image/*"}),
		Entry("Claude Agent", api.Anthropic, api.ModeAgent, []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}),
		Entry("Claude CLI", api.Anthropic, api.ModeCLI, []string{}),
		Entry("Gemini CLI", api.Google, api.ModeCLI, []string{}),
		Entry("cmux", api.OpenAI, api.ModeCmux, []string{}),
	)
})
