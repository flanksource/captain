package registry

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GPT-6 Astra", func() {
	const model = "gpt-6-astra"

	DescribeTable("resolves the exact ID and alias on every OpenAI runtime", func(mode RuntimeMode) {
		for _, token := range []string{model, "astra", "openai/" + model} {
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
		Expect(entry.Temperature).To(BeFalse())
		Expect(entry.SupportedEfforts).To(Equal([]Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}))
		price, ok := CostFor(model)
		Expect(ok).To(BeTrue())
		Expect(price).To(Equal(ModelCost{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5}))
	})

	It("sends reasoning effort without unsupported temperature", func() {
		temperature := 0.7
		Expect(OpenAI.GenerationConfig(ModeAPI, model, EffortHigh, 0, &temperature)).To(Equal(map[string]any{
			"reasoning_effort": "high",
		}))
	})
})
