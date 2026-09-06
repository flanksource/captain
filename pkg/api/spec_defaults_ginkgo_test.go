package api

import (
	"encoding/json"
	"errors"

	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Saved spec defaults", func() {
	It("fills the complete spec after authored layers and records equal-valued ownership", func() {
		saved := captainconfig.AIDefaults{DefaultModel: "api:sonnet:high", BudgetUSD: 2, MaxTokens: 1200, NoCache: true, NoSkills: true, Timeout: "2m"}
		project := PromptSpecLayer("project", Spec{Budget: Budget{Cost: 2}, Prompt: Prompt{User: "review"}})
		request := RequestSpecLayer("request", Spec{Budget: Budget{Cost: 2}})
		result, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{project, request}, Saved: &saved, RequireModel: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Spec.Name).To(Equal("claude-sonnet-5"))
		Expect(result.Spec.Mode).To(Equal(ModeAPI))
		Expect(result.Spec.Effort).To(Equal(EffortHigh))
		Expect(result.Spec.Prompt.User).To(Equal("review"))
		Expect(result.Spec.Budget).To(Equal(Budget{Cost: 2, MaxTokens: 1200, Timeout: "2m"}))
		Expect(result.Spec.Memory.SkipSkills).To(BeTrue())
		Expect(result.Provenance["/budget/cost"].Source).To(Equal(FieldSource{Kind: FieldSourceLayer, Name: "request", Key: "/budget/cost"}))
		Expect(result.Provenance["/budget/maxTokens"].Source.Key).To(Equal("ai.maxTokens"))
		Expect(result.Provenance["/model"].Source.Key).To(Equal("ai.defaultModel"))
		Expect(result.Provenance["/model"].NormalizedBy).NotTo(BeNil())
		Expect(result.Trace).To(Equal([]SpecLayer{project, request}))
	})

	It("keeps explicit false zero and empty fallbacks authoritative against saved defaults", func() {
		var request Spec
		Expect(json.Unmarshal([]byte(`{"model":"agent:sonnet","noCache":false,"fallbacks":[],"budget":{"cost":0},"memory":{"skipSkills":false}}`), &request)).To(Succeed())
		saved := captainconfig.AIDefaults{DefaultModel: "agent:sonnet,api:sol", NoCache: true, BudgetUSD: 4, NoSkills: true}
		result, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{RequestSpecLayer("request", request)}, Saved: &saved, RequireModel: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Spec.NoCache).To(BeFalse())
		Expect(result.Spec.Fallbacks).To(BeEmpty())
		Expect(result.Spec.Budget.Cost).To(BeZero())
		Expect(result.Spec.Memory.SkipSkills).To(BeFalse())
		for _, path := range []string{"/noCache", "/fallbacks", "/budget/cost", "/memory/skipSkills"} {
			Expect(result.Provenance[path].Source.Name).To(Equal("request"), path)
		}
	})

	It("uses each final candidate provider and identifies CSV versus list authorship", func() {
		saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
			"anthropic": {Mode: "agent", ReasoningEffort: "high"}, "openai": {Mode: "api", ReasoningEffort: "low"},
		}}
		profile := PromptSpecLayer("profile", Spec{Model: Model{Name: "sonnet,sol"}})
		request := RequestSpecLayer("request", Spec{Model: Model{Fallbacks: []Model{{Name: "haiku"}}}})
		result, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{profile, request}, Saved: &saved, RequireModel: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Spec.Fallbacks).To(HaveLen(2))
		Expect(result.Spec.Fallbacks[0].Mode).To(Equal(ModeAPI))
		Expect(result.Spec.Fallbacks[1].Mode).To(Equal(ModeAgent))
		Expect(result.Provenance["/fallbacks/0/model"].Source).To(Equal(FieldSource{Kind: FieldSourceLayer, Name: "profile", Key: "/model"}))
		Expect(result.Provenance["/fallbacks/1/model"].Source).To(Equal(FieldSource{Kind: FieldSourceLayer, Name: "request", Key: "/fallbacks/0/model"}))
		Expect(result.Provenance["/fallbacks/0/mode"].Source.Key).To(Equal("ai.providers.openai.mode"))
	})

	It("warns on every unconfigured candidate mode while retaining the compatibility runtime", func() {
		saved := captainconfig.AIDefaults{}
		result, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{RequestSpecLayer("request", Spec{Model: Model{Name: "sonnet,sol"}})}, Saved: &saved, RequireModel: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Warnings).To(HaveLen(2))
		Expect(result.Warnings[0]).To(ContainSubstring("configure"))
		Expect(result.Warnings[1]).To(ContainSubstring("fallbacks/0/mode"))
		Expect(result.Spec.Mode).To(Equal(ModeAgent))
	})

	It("requires a configured model only for generating runs", func() {
		saved := captainconfig.AIDefaults{}
		_, err := ResolveSpecLayers(ResolveSpecOptions{Saved: &saved, RequireModel: true})
		Expect(err).To(MatchError(ContainSubstring("configure")))
		result, err := ResolveSpecLayers(ResolveSpecOptions{Saved: &saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Spec.Name).To(BeEmpty())
		Expect(result.Warnings).To(BeEmpty())
	})

	It("keeps saved preview composition repairable until final runtime validation", func() {
		saved := captainconfig.AIDefaults{DefaultModel: "api:sonnet"}
		options := ResolveSpecOptions{Saved: &saved, Layers: []SpecLayer{PromptSpecLayer("profile", Spec{Sandbox: &SandboxRef{Mode: SandboxNative}})}}
		composed, err := ComposeSpecLayers(options)
		Expect(err).NotTo(HaveOccurred())
		Expect(composed.Spec.Mode).To(Equal(ModeAPI))
		_, err = ResolveSpecLayers(options)
		Expect(err).To(MatchError(ContainSubstring("sandbox mode")))
	})

	It("uses catalog effort and the existing token default after saved gaps, preserving explicit clears", func() {
		saved := captainconfig.AIDefaults{}
		layer := RequestSpecLayer("request", Spec{Model: Model{Name: "agent:fable"}})
		result, err := ResolveSpecLayers(ResolveSpecOptions{Saved: &saved, Layers: []SpecLayer{layer}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Spec.Budget.MaxTokens).To(Equal(4096))
		Expect(result.Spec.Effort).To(Equal(EffortHigh))
		Expect(result.Provenance["/budget/maxTokens"].Source.Kind).To(Equal(FieldSourceCatalog))
		Expect(result.Provenance["/effort"].Source.Kind).To(Equal(FieldSourceCatalog))
		layer.Spec = layer.Spec.WithExplicit("/budget/maxTokens", "/effort")
		result, err = ResolveSpecLayers(ResolveSpecOptions{Saved: &saved, Layers: []SpecLayer{layer}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Spec.Budget.MaxTokens).To(BeZero())
		Expect(result.Spec.Effort).To(BeEmpty())
		pure, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{layer}})
		Expect(err).NotTo(HaveOccurred())
		Expect(pure.Spec.Budget.MaxTokens).To(BeZero())
	})

	It("records a constraint that reduces a saved budget without rewriting its source", func() {
		saved := captainconfig.AIDefaults{BudgetUSD: 4}
		limit := SpecLayer{Name: "organization", Scope: SpecLayerGlobal, Constraints: RuntimeConstraints{Limits: RunLimits{Budget: Budget{Cost: 2}}}}
		result, err := ComposeSpecLayers(ResolveSpecOptions{Saved: &saved, Layers: []SpecLayer{limit}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Spec.Budget.Cost).To(Equal(float64(2)))
		Expect(result.Provenance["/budget/cost"]).To(Equal(FieldProvenance{
			Source:       FieldSource{Kind: FieldSourceSaved, Name: "~/.captain.yaml", Key: "ai.budgetUSD"},
			NormalizedBy: &FieldSource{Kind: FieldSourceLayer, Name: "organization", Key: "/constraints/limits/budget/cost"},
		}))
	})

	It("preserves the native MCP wire form and saved toggle provenance", func() {
		saved := captainconfig.AIDefaults{NoMCP: true}
		result, err := ComposeSpecLayers(ResolveSpecOptions{Saved: &saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Spec.Permissions.MCP.Disabled).To(BeTrue())
		Expect(result.Provenance["/permissions/mcp/disabled"].Source.Key).To(Equal("ai.noMCP"))
		encoded, err := json.Marshal(result.Spec)
		Expect(err).NotTo(HaveOccurred())
		var again Spec
		Expect(json.Unmarshal(encoded, &again)).To(Succeed())
		Expect(again.Permissions.MCP.Disabled).To(BeTrue())
		request := RequestSpecLayer("request", (Spec{Permissions: Permissions{MCP: MCP{Modes: ResourcePolicies{"example": ResourceEnabled}}}}).WithExplicit("/permissions/mcp/disabled"))
		result, err = ComposeSpecLayers(ResolveSpecOptions{Saved: &saved, Layers: []SpecLayer{request}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Spec.Permissions.MCP.Disabled).To(BeFalse())
		Expect(result.Spec.Permissions.MCP.Modes["example"]).To(Equal(ResourceEnabled))
	})

	It("attributes invalid saved declarations even when a request replaces them", func() {
		for _, saved := range []captainconfig.AIDefaults{{Temperature: 3}, {DefaultModel: "unknown-example-model"}} {
			_, err := ResolveSpecLayers(ResolveSpecOptions{Saved: &saved, Layers: []SpecLayer{RequestSpecLayer("request", Spec{Model: Model{Name: "agent:sonnet", Temperature: floatPtr(1)}})}})
			var configured *SavedDefaultsError
			Expect(errors.As(err, &configured)).To(BeTrue())
			Expect(configured.Source).To(Equal("~/.captain.yaml ai"))
		}
		saved := captainconfig.AIDefaults{}
		_, err := ResolveSpecLayers(ResolveSpecOptions{Saved: &saved, Layers: []SpecLayer{RequestSpecLayer("request", Spec{Model: Model{Name: "unknown-example-model"}})}})
		var configured *SavedDefaultsError
		Expect(errors.As(err, &configured)).To(BeFalse())
		Expect(err).To(HaveOccurred())
	})

	It("keeps unknown authored models visible in saved previews while final resolution rejects them", func() {
		saved := captainconfig.AIDefaults{MaxTokens: 500}
		layer := RequestSpecLayer("request", Spec{Model: Model{Name: "unknown-example-model"}})
		composed, err := ComposeSpecLayers(ResolveSpecOptions{Saved: &saved, Layers: []SpecLayer{layer}})
		Expect(err).NotTo(HaveOccurred())
		Expect(composed.Spec.Name).To(Equal("unknown-example-model"))
		Expect(composed.Spec.Budget.MaxTokens).To(Equal(500))
		Expect(composed.Trace).To(Equal([]SpecLayer{layer}))
		_, err = ResolveSpecLayers(ResolveSpecOptions{Saved: &saved, Layers: []SpecLayer{layer}})
		Expect(err).To(HaveOccurred())
	})
})
