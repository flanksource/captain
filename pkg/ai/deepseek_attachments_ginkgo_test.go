package ai_test

import (
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DeepSeek attachments", func() {
	imageTypes := []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

	DescribeTable("accepts supported images on Flash models", func(name string) {
		model := resolved(api.Model{Name: name, Mode: api.ModeAPI})
		Expect(model.MediaTypes).To(ConsistOf(imageTypes))
		for _, mediaType := range imageTypes {
			Expect(ai.ValidateAttachmentCompatibility([]api.Model{model}, []api.AttachmentRef{{MediaType: mediaType}})).To(Succeed())
		}
	},
		Entry("rolling Flash", "deepseek-flash"),
		Entry("V4 Flash alias", "deepseek-v4-flash"),
		Entry("vision experiment alias", "deepseek-v4-flash-vision-exp"),
	)

	It("keeps Pro text-only even when the API adapter can carry images", func() {
		model := resolved(api.Model{Name: "deepseek-v4-pro", Mode: api.ModeAPI})
		Expect(model.MediaTypes).To(BeEmpty())
		for _, mediaType := range imageTypes {
			Expect(ai.ValidateAttachmentCompatibility([]api.Model{model}, []api.AttachmentRef{{MediaType: mediaType}})).To(MatchError(ContainSubstring("does not accept " + mediaType)))
		}
	})

	DescribeTable("rejects unsupported attachment formats", func(mediaType string) {
		model := resolved(api.Model{Name: "deepseek-flash", Mode: api.ModeAPI})
		Expect(ai.ValidateAttachmentCompatibility([]api.Model{model}, []api.AttachmentRef{{MediaType: mediaType}})).To(MatchError(ContainSubstring("does not accept " + mediaType)))
	},
		Entry("SVG", "image/svg+xml"),
		Entry("PDF", "application/pdf"),
		Entry("audio", "audio/mpeg"),
		Entry("video", "video/mp4"),
	)

	It("publishes matching static and live catalog capabilities", func() {
		static := ai.CatalogInfo([]string{"deepseek"})
		live := ai.CatalogInfoFromAdapters([]ai.AdapterStatus{{
			Provider: "deepseek", Mode: "api",
			ModelDetails: []ai.ModelDef{
				{ID: "deepseek-flash", Provider: "deepseek", Mode: api.ModeAPI},
				{ID: "deepseek-v4-pro", Provider: "deepseek", Mode: api.ModeAPI},
			},
		}})
		for _, catalog := range [][]ai.ModelInfo{static, live} {
			Expect(catalog).To(ContainElement(And(
				HaveField("ID", "deepseek/deepseek-flash"),
				HaveField("InputMediaTypes", ConsistOf(imageTypes)),
				HaveField("Runtime.MediaTypes", ConsistOf(imageTypes)),
			)))
			Expect(catalog).To(ContainElement(And(
				HaveField("ID", "deepseek/deepseek-v4-pro"),
				HaveField("InputMediaTypes", BeEmpty()),
				HaveField("Runtime.MediaTypes", BeEmpty()),
			)))
		}
	})
})
