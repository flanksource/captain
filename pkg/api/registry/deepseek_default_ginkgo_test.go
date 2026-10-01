package registry

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DeepSeek default model", func() {
	const flash = "deepseek-flash"

	It("seeds the rolling flash alias on the API mode", func() {
		model, ok := DeepSeek.DefaultModel(ModeAPI)
		Expect(ok).To(BeTrue())
		Expect(model).To(Equal(flash))
	})

	It("keeps the pinned V4 flash id resolvable and unpreferred", func() {
		entry, ok := DeepSeek.Lookup("deepseek-v4-flash")
		Expect(ok).To(BeTrue())
		Expect(entry.Preferred).To(BeFalse())
		resolved, ok := DeepSeek.ResolveExact(ModeAPI, "deepseek-v4-flash")
		Expect(ok).To(BeTrue())
		Expect(resolved).To(Equal("deepseek-v4-flash"))
	})

	It("publishes the rolling alias capabilities and base token prices", func() {
		entry, ok := DeepSeek.Lookup(flash)
		Expect(ok).To(BeTrue())
		Expect(entry.Preferred).To(BeTrue())
		Expect(entry.Reasoning).To(BeTrue())
		Expect(entry.ContextWindow).To(Equal(1_000_000))
		price, ok := CostFor(flash)
		Expect(ok).To(BeTrue())
		Expect(price).To(Equal(ModelCost{Input: 0.15, Output: 0.6, CacheRead: 0.003}))
	})
})
