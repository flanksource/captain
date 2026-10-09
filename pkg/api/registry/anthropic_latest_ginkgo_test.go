package registry

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude Fable 5.1", func() {
	const model = "claude-fable-5-1"

	DescribeTable("resolves the bare Claude sentinel to Opus", func(mode RuntimeMode) {
		resolved, ok := Anthropic.ResolveExact(mode, "claude")
		Expect(ok).To(BeTrue())
		Expect(resolved).To(Equal("claude-opus-5-5"))
	},
		Entry("CLI", ModeCLI),
		Entry("agent", ModeAgent),
		Entry("cmux", ModeCmux),
	)

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

var _ = Describe("Claude Sonnet 5.5", func() {
	const model = "claude-sonnet-5-5"

	DescribeTable("resolves the latest Sonnet on every Claude runtime", func(mode RuntimeMode) {
		resolved, ok := Anthropic.ResolveExact(mode, "sonnet")
		Expect(ok).To(BeTrue())
		Expect(resolved).To(Equal(model))
		known, available := Anthropic.Availability(mode, model)
		Expect(known).To(BeTrue())
		Expect(available).To(BeTrue())
	},
		Entry("API", ModeAPI),
		Entry("CLI", ModeCLI),
		Entry("agent", ModeAgent),
		Entry("cmux", ModeCmux),
	)

	It("publishes Anthropic's current capabilities and price", func() {
		entry, ok := Anthropic.Lookup(model)
		Expect(ok).To(BeTrue())
		Expect(entry.Preferred).To(BeTrue())
		Expect(entry.ReleaseDate).To(Equal("2026-09-28"))
		Expect(entry.ContextWindow).To(Equal(1_000_000))
		Expect(entry.Temperature).To(BeFalse())
		Expect(entry.AdaptiveThinking).To(BeTrue())
		Expect(entry.SupportedEfforts).To(Equal([]Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}))
		Expect(entry.DefaultEffort).To(Equal(EffortHigh))
		price, ok := CostFor(model)
		Expect(ok).To(BeTrue())
		Expect(price).To(Equal(ModelCost{Input: 2, Output: 10, CacheRead: 0.1, CacheWrite: 2.5}))
	})

	It("uses adaptive thinking and omits unsupported temperature", func() {
		temperature := 0.7
		Expect(Anthropic.GenerationConfig(ModeAPI, model, EffortHigh, 4096, &temperature)).To(Equal(map[string]any{
			"max_tokens":    28672,
			"thinking":      map[string]any{"type": "adaptive"},
			"output_config": map[string]any{"effort": "high"},
		}))
	})
})

var _ = Describe("Claude Haiku 5.5", func() {
	const model = "claude-haiku-5-5"

	DescribeTable("resolves the latest Haiku on every Claude runtime", func(mode RuntimeMode) {
		for _, token := range []string{"haiku", model} {
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

	It("publishes Anthropic's current capabilities and base price", func() {
		entry, ok := Anthropic.Lookup(model)
		Expect(ok).To(BeTrue())
		Expect(entry.Preferred).To(BeTrue())
		Expect(entry.ReleaseDate).To(Equal("2026-10-07"))
		Expect(entry.ContextWindow).To(Equal(1_000_000))
		Expect(entry.Reasoning).To(BeTrue())
		Expect(entry.Temperature).To(BeFalse())
		Expect(entry.AdaptiveThinking).To(BeTrue())
		Expect(entry.InputMediaTypes).To(Equal([]string{"image/*", "application/pdf"}))
		Expect(entry.SupportedEfforts).To(Equal([]Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}))
		Expect(entry.DefaultEffort).To(Equal(EffortMedium))
		price, ok := CostFor(model)
		Expect(ok).To(BeTrue())
		Expect(price).To(Equal(ModelCost{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125}))
		previous, ok := Anthropic.Lookup("claude-haiku-4-5")
		Expect(ok).To(BeTrue())
		Expect(previous.Preferred).To(BeFalse())
	})

	It("uses adaptive thinking and omits unsupported temperature", func() {
		temperature := 0.7
		Expect(Anthropic.GenerationConfig(ModeAPI, model, EffortMedium, 4096, &temperature)).To(Equal(map[string]any{
			"max_tokens":    12288,
			"thinking":      map[string]any{"type": "adaptive"},
			"output_config": map[string]any{"effort": "medium"},
		}))
		Expect(Anthropic.GenerationConfig(ModeAPI, model, EffortNone, 4096, nil)).To(Equal(map[string]any{"max_tokens": 4096}))
	})
})
