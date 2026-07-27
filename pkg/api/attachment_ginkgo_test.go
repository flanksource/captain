package api_test

import (
	"strings"

	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AttachmentRef", func() {
	It("requires exactly one source", func() {
		ref := api.AttachmentRef{Path: "invoice.pdf", URL: "https://example.com/invoice.pdf"}
		Expect(ref.Validate()).To(MatchError(ContainSubstring("exactly one source")))
	})

	It("accepts a durable attachment id", func() {
		ref := api.AttachmentRef{ID: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
		Expect(ref.Validate()).To(Succeed())
	})
})

var _ = Describe("Prompt attachment validation", func() {
	It("accepts an attachment-only prompt", func() {
		prompt := api.Prompt{Attachments: []api.AttachmentRef{{Path: "invoice.pdf"}}}
		Expect(prompt.Validate()).To(Succeed())
	})

	It("does not classify an attachment-only verification request as verify-only", func() {
		spec := api.Spec{
			Prompt:   api.Prompt{Attachments: []api.AttachmentRef{{Path: "invoice.pdf"}}},
			Workflow: &api.Workflow{Verify: &api.Verify{}},
		}
		Expect(spec.IsVerifyOnly()).To(BeFalse())
	})

	It("includes durable attachment descriptors in the cache identity", func() {
		first := api.Prompt{User: "compare", Attachments: []api.AttachmentRef{{ID: api.AttachmentIDPrefix + strings.Repeat("a", 64), MediaType: "image/png"}}}
		second := api.Prompt{User: "compare", Attachments: []api.AttachmentRef{{ID: api.AttachmentIDPrefix + strings.Repeat("b", 64), MediaType: "image/png"}}}
		Expect(first.CacheIdentity()).NotTo(Equal(second.CacheIdentity()))
		Expect(first.CacheIdentity()).NotTo(ContainSubstring("base64"))
	})
})
