package genkit

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
)

var _ = Describe("promptParts", func() {
	It("preserves text followed by ordered prepared media parts", func() {
		first := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}.
			WithPreparedContent(api.AttachmentContent{Bytes: []byte("first")})
		second := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "application/pdf"}.
			WithPreparedContent(api.AttachmentContent{Bytes: []byte("second")})

		parts, err := promptParts(ai.Request{Prompt: api.Prompt{User: "compare", Attachments: []api.AttachmentRef{first, second}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(HaveLen(3))
		Expect(parts[0].Kind).To(Equal(gkai.PartText))
		Expect(parts[0].Text).To(Equal("compare"))
		Expect(parts[1].ContentType).To(Equal("image/png"))
		Expect(parts[1].Text).To(Equal("data:image/png;base64,Zmlyc3Q="))
		Expect(parts[2].ContentType).To(Equal("application/pdf"))
		Expect(parts[2].Text).To(Equal("data:application/pdf;base64,c2Vjb25k"))
	})

	It("supports a file-only prompt", func() {
		attachment := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}.
			WithPreparedContent(api.AttachmentContent{Bytes: []byte("image")})
		parts, err := promptParts(ai.Request{Prompt: api.Prompt{Attachments: []api.AttachmentRef{attachment}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(HaveLen(1))
		Expect(parts[0].Kind).To(Equal(gkai.PartMedia))
	})

	It("fails loudly when resolution was skipped", func() {
		attachment := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}
		_, err := promptParts(ai.Request{Prompt: api.Prompt{Attachments: []api.AttachmentRef{attachment}}})
		Expect(err).To(MatchError(ContainSubstring("is not prepared")))
	})
})
