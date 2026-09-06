package api

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Hierarchical spec profiles", func() {
	It("orders layers by scope and structurally overlays defaults without mutating inputs", func() {
		global := SpecLayer{
			Name: "platform", Scope: SpecLayerGlobal,
			Spec: Spec{Model: Model{Name: "claude-sonnet-5"}, Budget: Budget{MaxTokens: 8000}},
		}
		context := SpecLayer{
			Name: "claims", Scope: SpecLayerContext,
			Spec: Spec{Model: Model{Effort: EffortHigh}, Budget: Budget{MaxTurns: 6}},
		}
		surface := PromptSpecLayer("triage.prompt", Spec{Prompt: Prompt{System: "Triage claims."}})
		user := SpecLayer{
			Name: "request", Scope: SpecLayerUser,
			Spec: Spec{Model: Model{Effort: EffortLow}},
		}

		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{user, surface, context, global}})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Model.Name).To(Equal("claude-sonnet-5"))
		Expect(resolved.Spec.Model.Effort).To(Equal(EffortLow))
		Expect(resolved.Spec.Budget).To(Equal(Budget{MaxTokens: 8000, MaxTurns: 6}))
		Expect(resolved.Spec.Prompt.System).To(Equal("Triage claims."))
		Expect(resolved.Trace).To(HaveLen(4))
		Expect([]SpecLayerScope{
			resolved.Trace[0].Scope, resolved.Trace[1].Scope, resolved.Trace[2].Scope, resolved.Trace[3].Scope,
		}).To(Equal([]SpecLayerScope{SpecLayerGlobal, SpecLayerContext, SpecLayerSurface, SpecLayerUser}))
		Expect(context.Spec.Model.Name).To(BeEmpty())
		Expect(global.Spec.Model.Effort).To(BeEmpty())
	})

	It("intersects restrictive model catalogs and validates every fallback", func() {
		layers := []SpecLayer{
			{
				Name: "platform", Scope: SpecLayerGlobal,
				Constraints: RuntimeConstraints{Models: []string{"claude-sonnet-5", "gpt-5.6-sol", "gpt-5.4"}},
			},
			{
				Name: "claims", Scope: SpecLayerContext,
				Constraints: RuntimeConstraints{Models: []string{"gpt-5.6-sol", "claude-sonnet-5"}},
			},
			{
				Name: "request", Scope: SpecLayerUser,
				Spec: Spec{Model: Model{Name: "gpt-5.6-sol", Fallbacks: []Model{{Name: "claude-sonnet-5"}}}},
			},
		}

		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: layers})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Constraints.Models).To(Equal([]string{"claude-sonnet-5", "gpt-5.6-sol"}))
		Expect(resolved.AllowsModel(Model{Name: "gpt-5.4"})).To(BeFalse())
		Expect(resolved.AllowsModel(Model{Name: "gpt-5.6-sol"})).To(BeTrue())

		layers[2].Spec.Model.Fallbacks = []Model{{Name: "gpt-5.4"}}
		_, err = ResolveSpecLayers(ResolveSpecOptions{Layers: layers})
		Expect(err).To(MatchError(ContainSubstring(`fallback model "gpt-5.4" is outside the effective model catalog`)))
	})

	It("normalizes model selectors before intersecting catalogs", func() {
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
			SpecLayer{
				Name: "platform", Scope: SpecLayerGlobal,
				Constraints: RuntimeConstraints{Models: []string{" gpt-5.4 "}},
			},
			SpecLayer{
				Name: "claims", Scope: SpecLayerContext,
				Constraints: RuntimeConstraints{Models: []string{"gpt-5.4"}},
			},
		}})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Constraints.Models).To(Equal([]string{"gpt-5.4"}))
	})

	It("uses strict non-zero run ceilings and retains each named quota independently", func() {
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
			SpecLayer{
				Name: "platform", Scope: SpecLayerGlobal,
				Spec: Spec{Budget: Budget{Cost: 12, MaxTokens: 9000, MaxTurns: 10, Timeout: "10m"}},
				Constraints: RuntimeConstraints{
					Limits: RunLimits{MaxInputTokens: 12000, Budget: Budget{Cost: 8, MaxTokens: 7000, MaxTurns: 8, Timeout: "8m"}},
					Quotas: []UsageQuota{{Name: "platform-monthly", TokenLimit: 1_000_000, TokensUsed: 10}},
				},
			},
			SpecLayer{
				Name: "claims", Scope: SpecLayerContext,
				Spec: Spec{Budget: Budget{Cost: 10, MaxTokens: 8000, MaxTurns: 6, Timeout: "9m"}},
				Constraints: RuntimeConstraints{
					Limits: RunLimits{MaxInputTokens: 4000, Budget: Budget{Cost: 5, MaxTokens: 6000, MaxTurns: 7, Timeout: "5m"}},
					Quotas: []UsageQuota{{Name: "claims-monthly", CostLimitUSD: 50, CostUsedUSD: 2}},
				},
			},
		}})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Budget).To(Equal(Budget{Cost: 5, MaxTokens: 6000, MaxTurns: 6, Timeout: "5m"}))
		Expect(resolved.Constraints.Limits.MaxInputTokens).To(Equal(4000))
		Expect(resolved.Constraints.Quotas).To(Equal([]UsageQuota{
			{Name: "platform-monthly", Scope: SpecLayerGlobal, Layer: "platform", TokenLimit: 1_000_000, TokensUsed: 10},
			{Name: "claims-monthly", Scope: SpecLayerContext, Layer: "claims", CostLimitUSD: 50, CostUsedUSD: 2},
		}))
		duration, err := time.ParseDuration(resolved.Spec.Budget.Timeout)
		Expect(err).NotTo(HaveOccurred())
		Expect(duration).To(Equal(5 * time.Minute))
	})
})
