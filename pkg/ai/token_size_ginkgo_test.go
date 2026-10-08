package ai_test

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Token sizing", func() {
	It("estimates full UTF-8 text offline and prices output separately", func() {
		result, err := ai.SizeTokens(context.Background(), ai.TokenSizeOptions{
			Request: api.Spec{Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAgent}, Prompt: api.Prompt{User: "你好世界"}},
			Method:  "estimate", Direction: "output",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Usage).To(Equal(api.Usage{OutputTokens: 3}))
		Expect(result.Source).To(Equal("local-estimate"))
		Expect(result.CostUSD).NotTo(BeNil())
		Expect(*result.CostUSD).To(BeNumerically("~", 0.000045, 1e-12))
		Expect(result.Coverage.Partial).To(BeTrue())
	})

	It("does not guess attachment tokens from byte size", func() {
		result, err := ai.SizeTokens(context.Background(), ai.TokenSizeOptions{
			Request: api.Spec{Model: api.Model{Name: "gemini-2.5-pro", Mode: api.ModeAPI}, Prompt: api.Prompt{User: "test", Attachments: []api.AttachmentRef{{Path: "unavailable.png", MediaType: "image/png", Size: 100000}}}},
			Method:  "estimate",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.TotalTokens).To(Equal(1))
		Expect(result.Coverage.Excluded).To(ContainElement("attachments"))
	})

	It("rejects invalid methods and directions", func() {
		_, err := ai.SizeTokens(context.Background(), ai.TokenSizeOptions{Method: "generate"})
		Expect(err).To(MatchError(ContainSubstring("method")))
		_, err = ai.SizeTokens(context.Background(), ai.TokenSizeOptions{Method: "estimate", Direction: "cache"})
		Expect(err).To(MatchError(ContainSubstring("direction")))
	})

	It("returns cancellation before doing work", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := ai.SizeTokens(ctx, ai.TokenSizeOptions{Method: "estimate"})
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	It("refuses DeepSeek provider counting without generating or resolving credentials", func() {
		_, err := ai.SizeTokens(context.Background(), ai.TokenSizeOptions{Method: "provider", Request: api.Spec{Model: api.Model{Name: "deepseek-chat", Mode: api.ModeAPI}, Prompt: api.Prompt{User: "test"}}})
		Expect(errors.Is(err, api.ErrTokenCountingUnsupported)).To(BeTrue())
	})
})
