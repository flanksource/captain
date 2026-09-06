package registry

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude Fable 5.1", func() {
	const model = "claude-fable-5-1"

	DescribeTable("resolves the latest family on every Claude runtime", func(mode RuntimeMode) {
		for _, token := range []string{"fable", model} {
			resolved, ok := Anthropic.ResolveExact(mode, token)
			Expect(ok).To(BeTrue())
			Expect(resolved).To(Equal(model))
		}
		known, available := Anthropic.Availability(mode, model)
		Expect(known).To(BeTrue())
		Expect(available).To(BeTrue())
	},
		Entry("API", ModeAPI),
		Entry("CLI", ModeCLI),
		Entry("agent", ModeAgent),
		Entry("cmux", ModeCmux),
	)

	It("publishes the current capabilities and cache price", func() {
		entry, ok := Anthropic.Lookup(model)
		Expect(ok).To(BeTrue())
		Expect(entry.Preferred).To(BeTrue())
		Expect(entry.ContextWindow).To(Equal(1_000_000))
		Expect(entry.ReleaseDate).To(Equal("2026-09-01"))
		Expect(entry.Temperature).To(BeFalse())
		Expect(entry.AdaptiveThinking).To(BeTrue())
		Expect(entry.SupportedEfforts).To(Equal([]Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}))
		Expect(entry.DefaultEffort).To(Equal(EffortHigh))
		price, ok := CostFor(model)
		Expect(ok).To(BeTrue())
		Expect(price).To(Equal(ModelCost{Input: 10, Output: 50, CacheRead: 0.25, CacheWrite: 12.5}))
		previousPrice, ok := CostFor("claude-fable-5")
		Expect(ok).To(BeTrue())
		Expect(previousPrice).To(Equal(ModelCost{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5}))
	})

	It("uses adaptive thinking and omits unsupported temperature", func() {
		temperature := 0.7
		Expect(Anthropic.GenerationConfig(ModeAPI, model, EffortHigh, 4096, &temperature)).To(Equal(map[string]any{
			"max_tokens":    28672,
			"thinking":      map[string]any{"type": "adaptive"},
			"output_config": map[string]any{"effort": "high"},
		}))
		Expect(Anthropic.GenerationConfig(ModeAPI, model, EffortNone, 4096, nil)).To(Equal(map[string]any{"max_tokens": 4096}))
	})
})
