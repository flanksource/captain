package aichat

import (
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// captain_session_costs.total_cost resolves provider-reported against list-price
// per underlying call, so it is non-zero either way. Carrying it as the
// aggregate's ProviderCostUSD — which three separate projections did — makes a
// pure reconstruction indistinguishable from a billed figure, and every renderer
// that marks estimates then silently presents one as the other.
var _ = Describe("thread cost aggregation", func() {
	const (
		inputCost      = 0.75
		outputCost     = 1.25
		cacheReadCost  = 0.50
		bucketTotal    = inputCost + outputCost + cacheReadCost
		providerBilled = 3.10
	)

	listPriced := database.SessionCost{
		Model: "claude-opus-5", InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 5000,
		InputCost: inputCost, OutputCost: outputCost, CacheReadCost: cacheReadCost,
		TotalCost: bucketTotal, ProviderCostUSD: 0,
	}
	// A provider-reported call stores its buckets too; total_cost prefers the
	// billed figure over their sum, and so must the aggregate.
	providerReported := database.SessionCost{
		Model: "claude-opus-5", InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 5000,
		InputCost: inputCost, OutputCost: outputCost, CacheReadCost: cacheReadCost,
		TotalCost: providerBilled, ProviderCostUSD: providerBilled,
	}

	It("leaves a list-priced thread with no provider cost, so it renders as an estimate", func() {
		aggregate := &session.Session{}
		applyThreadCosts(aggregate, []database.SessionCost{listPriced})

		Expect(aggregate.Cost.ProviderCostUSD).To(BeZero())
		Expect(aggregate.Cost.Total()).To(BeNumerically("~", bucketTotal, 1e-9))
	})

	It("keeps a provider-reported thread's billed total in preference to its buckets", func() {
		aggregate := &session.Session{}
		applyThreadCosts(aggregate, []database.SessionCost{providerReported})

		Expect(aggregate.Cost.ProviderCostUSD).To(BeNumerically("~", providerBilled, 1e-9))
		Expect(aggregate.Cost.Total()).To(BeNumerically("~", providerBilled, 1e-9))
	})

	It("reports usage summed across the thread's models", func() {
		aggregate := &session.Session{}
		applyThreadCosts(aggregate, []database.SessionCost{listPriced, providerReported})

		Expect(aggregate.Usage.InputTokens).To(Equal(2000))
		Expect(aggregate.Usage.OutputTokens).To(Equal(400))
		Expect(aggregate.Usage.CacheReadTokens).To(Equal(10000))
	})
})
