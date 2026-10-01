package registry

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GPT-6.1 Sol", func() {
	const model = "gpt-6.1-sol"

	DescribeTable("resolves the current Sol on every OpenAI runtime", func(mode RuntimeMode) {
		for _, token := range []string{model, "sol", "codex", "openai/" + model} {
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

	It("publishes its documented capabilities and base token prices", func() {
		entry, ok := OpenAI.Lookup(model)
		Expect(ok).To(BeTrue())
		Expect(entry.Preferred).To(BeTrue())
		Expect(entry.ContextWindow).To(Equal(1_050_000))
		Expect(entry.Reasoning).To(BeTrue())
		Expect(entry.Temperature).To(BeFalse())
		Expect(entry.SupportedEfforts).To(Equal([]Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}))
		Expect(entry.DefaultEffort).To(Equal(EffortMedium))
		price, ok := CostFor(model)
		Expect(ok).To(BeTrue())
		Expect(price).To(Equal(ModelCost{Input: 2, Output: 10, CacheRead: 0.1, CacheWrite: 2.5}))
	})

	It("sends reasoning effort without unsupported temperature", func() {
		temperature := 0.7
		Expect(OpenAI.GenerationConfig(ModeAPI, model, EffortHigh, 0, &temperature)).To(Equal(map[string]any{
			"reasoning_effort": "high",
		}))
	})

	It("keeps explicit GPT-6 Sol selections at their original model and price", func() {
		resolved, ok := OpenAI.ResolveExact(ModeAPI, "gpt-6-sol")
		Expect(ok).To(BeTrue())
		Expect(resolved).To(Equal("gpt-6-sol"))
		price, ok := CostFor(resolved)
		Expect(ok).To(BeTrue())
		Expect(price).To(Equal(ModelCost{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5}))
	})
})
