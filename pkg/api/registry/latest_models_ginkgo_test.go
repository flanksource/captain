package registry

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("current preferred models", func() {
	DescribeTable("resolves provider picks on their supported runtimes", func(provider *Provider, mode RuntimeMode, token, want string) {
		resolved, ok := provider.ResolveExact(mode, token)
		Expect(ok).To(BeTrue())
		Expect(resolved).To(Equal(want))
	},
		Entry("Codex agent", OpenAI, ModeAgent, "codex", "gpt-6-sol"),
		Entry("Codex CLI", OpenAI, ModeCLI, "codex", "gpt-6-sol"),
		Entry("Codex cmux", OpenAI, ModeCmux, "codex", "gpt-6-sol"),
		Entry("Codex API", OpenAI, ModeAPI, "codex", "gpt-6-sol"),
		Entry("Codex Sol alias", OpenAI, ModeCLI, "sol", "gpt-6-sol"),
		Entry("Codex Luna alias", OpenAI, ModeCmux, "luna", "gpt-6-luna"),
		Entry("Claude agent", Anthropic, ModeAgent, "claude", "claude-opus-5-5"),
		Entry("Claude Opus", Anthropic, ModeAPI, "opus", "claude-opus-5-5"),
		Entry("DeepSeek", DeepSeek, ModeAPI, "deepseek", "deepseek-flash"),
	)

	DescribeTable("publishes preferred models with current list prices", func(provider *Provider, id string, priority int, cost ModelCost) {
		model, ok := provider.Lookup(id)
		Expect(ok).To(BeTrue())
		Expect(model.Preferred).To(BeTrue())
		Expect(model.Priority).To(Equal(priority))
		Expect(model.Cost).To(Equal(&cost))
	},
		Entry("Astra", OpenAI, "gpt-6-astra", 2, ModelCost{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5}),
		Entry("Sol", OpenAI, "gpt-6-sol", 1, ModelCost{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5}),
		Entry("Luna", OpenAI, "gpt-6-luna", 3, ModelCost{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125}),
		Entry("Opus", Anthropic, "claude-opus-5-5", 1, ModelCost{Input: 4, Output: 20, CacheRead: 0.2, CacheWrite: 5}),
		Entry("Flash", DeepSeek, "deepseek-flash", 1, ModelCost{Input: 0.15, Output: 0.6, CacheRead: 0.003}),
	)

	It("keeps older generations available without preferring them", func() {
		for _, id := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "claude-opus-5", "claude-fable-5", "claude-opus-4-8"} {
			provider, err := ProviderFor(id)
			Expect(err).NotTo(HaveOccurred())
			model, ok := provider.Lookup(id)
			Expect(ok).To(BeTrue())
			Expect(model.Preferred).To(BeFalse())
		}
	})

	It("uses Opus 5.5's documented default effort", func() {
		model, ok := Anthropic.Lookup("claude-opus-5-5")
		Expect(ok).To(BeTrue())
		Expect(model.DefaultEffort).To(Equal(EffortMedium))
		Expect(model.AdaptiveThinking).To(BeTrue())
	})
})
