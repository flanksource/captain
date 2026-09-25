package api

import (
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

	// Layers default; they do not constrain. A posture authored anywhere in the
	// stack is a value the next layer may replace with any other valid posture,
	// including one no ordering relates it to. A host that must not be widened
	// applies its posture as the last layer instead.
	DescribeTable("lets the last layer naming a posture decide it", func(global, surface, expected PermissionMode) {
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
			{Name: ".gavel.yaml ai", Scope: SpecLayerGlobal, Spec: Spec{Permissions: Permissions{Mode: global}}},
			PromptSpecLayer("todos-triage.prompt", Spec{Permissions: Permissions{Mode: surface}}),
		}})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Permissions.Mode).To(Equal(expected))
	},
		Entry("unordered posture below a read-only prompt", PermissionAuto, PermissionPlan, PermissionPlan),
		Entry("unordered posture below a widening prompt", PermissionDontAsk, PermissionBypass, PermissionBypass),
		Entry("read-only posture below a widening prompt", PermissionPlan, PermissionAcceptEdits, PermissionAcceptEdits),
		Entry("widening posture below a read-only prompt", PermissionBypass, PermissionPlan, PermissionPlan),
		Entry("prompt that names no posture keeps the default below it", PermissionAuto, PermissionMode(""), PermissionAuto),
	)

	It("lets a later layer restore a tool an earlier layer denied", func() {
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
			{
				Name: "platform", Scope: SpecLayerGlobal,
				Spec: Spec{Permissions: Permissions{Mode: PermissionPlan, Tools: Tools{"Bash": ToolPolicyDeny}}},
			},
			RequestSpecLayer("request", Spec{Permissions: Permissions{Tools: Tools{"Bash": ToolPolicyAllow}}}),
		}})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Permissions.Tools["Bash"]).To(Equal(ToolPolicyAllow))
	})

	It("takes each budget field from the last layer that names it", func() {
		resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
			{
				Name: "platform", Scope: SpecLayerGlobal,
				Spec: Spec{Budget: Budget{Cost: 8, MaxTokens: 7000, MaxTurns: 8, Timeout: "8m"}},
			},
			{
				Name: "claims", Scope: SpecLayerContext,
				Spec: Spec{Budget: Budget{Cost: 12, Timeout: "20m"}},
			},
		}})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Spec.Budget).To(Equal(Budget{Cost: 12, MaxTokens: 7000, MaxTurns: 8, Timeout: "20m"}))
	})
})
