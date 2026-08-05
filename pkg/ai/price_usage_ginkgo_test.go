package ai

import (
	"github.com/flanksource/captain/pkg/ai/pricing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Observed on aichat thread 6f58a8f6: one claude-opus-5 turn on the anthropic
// backend. Opus 5 lists at $5/Mtok input and $25/Mtok output, so the turn's
// cost splits 0.640690 + 0.091700 = 0.732390 — the figure the UI rendered as a
// single opaque total while every per-bucket row showed "-".
const (
	opusModel        = "claude-opus-5"
	turnInputTokens  = 128138
	turnOutputTokens = 3668
	turnInputUSD     = 0.640690
	turnOutputUSD    = 0.091700
)

var _ = Describe("PriceUsage", func() {
	BeforeEach(func() {
		pricing.EnsureLoaded(pricing.LoadOptions{})
	})

	It("splits a turn across the per-bucket costs instead of collapsing it into one", func() {
		usage := Usage{InputTokens: turnInputTokens, OutputTokens: turnOutputTokens}

		cost := PriceUsage(BackendAnthropic, opusModel, usage, 0)

		Expect(cost.InputCost).To(BeNumerically("~", turnInputUSD, 1e-6))
		Expect(cost.OutputCost).To(BeNumerically("~", turnOutputUSD, 1e-6))
		Expect(cost.Total()).To(BeNumerically("~", turnInputUSD+turnOutputUSD, 1e-6))
	})

	It("prices cache reads and writes into their own buckets", func() {
		// The claude-agent side of the same thread: near-zero input against a
		// large cached prefix. Folding cache into input would misprice it by
		// 10x, since cache reads list at $0.50/Mtok against input's $5.00.
		// Expectations below are computed from those list rates, not from the
		// function's own output.
		const (
			agentInput      = 74
			agentOutput     = 31502
			agentCacheRead  = 3108284
			agentCacheWrite = 185751

			expectCacheReadUSD  = agentCacheRead * 0.50 / 1e6  // 1.554142
			expectCacheWriteUSD = agentCacheWrite * 6.25 / 1e6 // 1.160944
		)
		usage := Usage{
			InputTokens: agentInput, OutputTokens: agentOutput,
			CacheReadTokens: agentCacheRead, CacheWriteTokens: agentCacheWrite,
		}

		cost := PriceUsage(BackendClaudeAgent, opusModel, usage, 0)

		Expect(cost.CacheReadCost).To(BeNumerically("~", expectCacheReadUSD, 1e-6))
		Expect(cost.CacheWriteCost).To(BeNumerically("~", expectCacheWriteUSD, 1e-6))
		Expect(cost.TotalTokens).To(Equal(agentInput + agentOutput + agentCacheRead + agentCacheWrite))
	})

	It("prefers the provider's reported total over the list-price recompute", func() {
		// The provider figure covers pricing captain's registry cannot model,
		// such as 1-hour cache writes billed above the 5-minute rate.
		usage := Usage{InputTokens: turnInputTokens, OutputTokens: turnOutputTokens}
		const providerReported = 4.295061

		cost := PriceUsage(BackendClaudeAgent, opusModel, usage, providerReported)

		Expect(cost.ProviderCostUSD).To(Equal(providerReported))
		Expect(cost.Total()).To(Equal(providerReported))
		Expect(cost.InputCost).To(BeNumerically("~", turnInputUSD, 1e-6),
			"the list-price breakdown is still retained for display")
	})

	It("keeps token counts when the model is absent from the pricing registry", func() {
		usage := Usage{InputTokens: 10, OutputTokens: 20}

		cost := PriceUsage(BackendAnthropic, "model-that-does-not-exist", usage, 0)

		Expect(cost.TotalTokens).To(Equal(30))
		Expect(cost.Total()).To(BeZero())
	})
})
