package registry

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GPT-6.1 Terra", func() {
	const model = "gpt-6.1-terra"

	DescribeTable("resolves Terra on every OpenAI runtime", func(mode RuntimeMode) {
		for _, token := range []string{model, "terra", "openai/" + model} {
			resolved, ok := OpenAI.ResolveExact(mode, token)
			Expect(ok).To(BeTrue())
			Expect(resolved).To(Equal(model))
		}
		known, available := OpenAI.Availability(mode, model)
		Expect(known).To(BeTrue())
		Expect(available).To(BeTrue())
	},
		Entry("API", ModeAPI),
		Entry("CLI", ModeCLI),
		Entry("agent", ModeAgent),
		Entry("cmux", ModeCmux),
	)

	It("publishes its capabilities and base token prices", func() {
		entry, ok := OpenAI.Lookup(model)
		Expect(ok).To(BeTrue())
		Expect(entry.Preferred).To(BeTrue())
		Expect(entry.ContextWindow).To(Equal(1_050_000))
		Expect(entry.Reasoning).To(BeTrue())
		Expect(entry.Temperature).To(BeFalse())
		Expect(entry.SupportedEfforts).To(Equal([]Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}))
		Expect(entry.DefaultEffort).To(Equal(EffortMedium))
		price, ok := CostFor("terra")
		Expect(ok).To(BeTrue())
		Expect(price).To(Equal(ModelCost{Input: 1, Output: 5, CacheRead: 0.05, CacheWrite: 1.25}))
	})
})
