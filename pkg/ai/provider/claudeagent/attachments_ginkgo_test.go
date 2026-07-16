package claudeagent

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Claude Agent prompt parameters", func() {
	It("derives the required SDK version from the embedded package manifest", func() {
		version, err := requiredSDKVersion()
		Expect(err).NotTo(HaveOccurred())
		Expect(version).To(Equal("0.3.210"))
	})

	It("materializes the structured attachment bridge source", func() {
		directory, err := prepareAgentDir()
		Expect(err).NotTo(HaveOccurred())
		content, err := os.ReadFile(filepath.Join(directory, "agent.ts"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("attachments?: PromptAttachment[]"))
	})

	It("encodes prepared image and PDF data as ordered structured inputs", func() {
		image := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}.
			WithPreparedContent(api.AttachmentContent{Bytes: []byte("image")})
		pdf := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "application/pdf"}.
			WithPreparedContent(api.AttachmentContent{Bytes: []byte("pdf")})

		params, err := buildPromptParams(ai.Request{Prompt: api.Prompt{User: "inspect", Attachments: []api.AttachmentRef{image, pdf}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(params.Text).To(Equal("inspect"))
		Expect(params.Attachments).To(Equal([]promptAttachment{
			{MediaType: "image/png", Data: "aW1hZ2U="},
			{MediaType: "application/pdf", Data: "cGRm"},
		}))
	})
})
