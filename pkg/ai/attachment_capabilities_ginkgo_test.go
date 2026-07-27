package ai_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Attachment capabilities", func() {
	It("clamps catalog modalities to what the backend adapter can execute", func() {
		DeferCleanup(ai.ResetModelCatalog)
		model := ai.Model{
			ID:              "openai/vision-test",
			Backend:         ai.BackendOpenAI,
			InputMediaTypes: []string{"image/*", "audio/*"},
		}
		Expect(ai.SetModelCatalog([]ai.Model{model})).To(Succeed())
		info := ai.CatalogInfo([]string{"openai"})
		Expect(info).To(HaveLen(1))
		Expect(info[0].InputMediaTypes).To(Equal([]string{"image/*"}))
	})

	It("fails the entire multi-model request when one backend is incompatible", func() {
		models := []api.Model{
			{Name: "vision-test", Backend: api.BackendCodexAgent},
			{Name: "gemini-3.1-pro", Backend: api.BackendGeminiCLI},
		}
		refs := []api.AttachmentRef{{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}}

		err := ai.ValidateAttachmentCompatibility(models, refs)
		Expect(err).To(MatchError(ContainSubstring("gemini-cli:gemini-3.1-pro does not accept image/png attachments")))
	})

	It("rejects image formats the Claude Agent SDK cannot encode", func() {
		err := ai.ValidateAttachmentCompatibility(
			[]api.Model{{Name: "claude-sonnet-5", Backend: api.BackendClaudeAgent}},
			[]api.AttachmentRef{{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/svg+xml"}},
		)
		Expect(err).To(MatchError(ContainSubstring("does not accept image/svg+xml attachments")))
	})

	It("expands a model image wildcard to the Claude adapter formats", func() {
		model := api.Model{Name: "claude-sonnet-5", Backend: api.BackendClaudeAgent}
		ref := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}
		Expect(ai.ValidateAttachmentCompatibility([]api.Model{model}, []api.AttachmentRef{ref})).To(Succeed())
	})

	DescribeTable("publishes the adapter capability matrix",
		func(backend api.Backend, expected []string) {
			Expect(ai.AdapterInputMediaTypes(backend)).To(Equal(expected))
		},
		Entry("Gemini API", api.BackendGemini, []string{"image/*", "audio/*", "video/*", "application/pdf"}),
		Entry("Anthropic API", api.BackendAnthropic, []string{"image/*"}),
		Entry("OpenAI API", api.BackendOpenAI, []string{"image/*"}),
		Entry("DeepSeek API", api.BackendDeepSeek, []string{}),
		Entry("Codex agent", api.BackendCodexAgent, []string{"image/*"}),
		Entry("Codex CLI", api.BackendCodexCLI, []string{"image/*"}),
		Entry("Claude Agent", api.BackendClaudeAgent, []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}),
		Entry("Claude CLI", api.BackendClaudeCLI, []string{}),
		Entry("Gemini CLI", api.BackendGeminiCLI, []string{}),
		Entry("cmux", api.BackendCodexCmux, []string{}),
	)
})
