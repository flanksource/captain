package aiflags

import (
	"fmt"
	"testing"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDefaultResolution(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Final model defaults")
}

var _ = Describe("final model saved defaults", func() {
	DescribeTable("keeps unknown authored candidates repairable only for partial composition", func(allow bool) {
		input := registry.Model{Name: "unknown-primary-example", Fallbacks: registry.ModelList{{Name: "unknown-fallback-example"}}}
		result, err := ApplyDefaults(DefaultOptions{Model: input, Saved: captainconfig.AIDefaults{Temperature: 0.4, NoCache: true}, CatalogDefaults: true, AllowUnknownModel: allow})
		if !allow {
			Expect(registry.IsUnknownModel(err)).To(BeTrue())
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Name).To(Equal(input.Name))
		Expect(result.Model.Fallbacks[0].Name).To(Equal(input.Fallbacks[0].Name))
		for _, candidate := range []registry.Model{result.Model, result.Model.Fallbacks[0]} {
			Expect(candidate.Mode).To(BeEmpty())
			Expect(candidate.Effort).To(BeEmpty())
			Expect(*candidate.Temperature).To(Equal(0.4))
			Expect(candidate.NoCache).To(BeTrue())
		}
	}, Entry("partial composition", true), Entry("direct run resolution", false))

	It("still rejects malformed saved configuration while unknown authored candidates are allowed", func() {
		_, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "unknown-example"}, Saved: captainconfig.AIDefaults{DefaultModel: "unknown-saved-example"}, AllowUnknownModel: true})
		Expect(err).To(MatchError(ContainSubstring("ai.defaultModel")))
	})

	It("applies each candidate's own provider settings and records their exact keys", func() {
		saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
			"anthropic": {Mode: "agent", ReasoningEffort: "high"},
			"openai":    {Mode: "api", ReasoningEffort: "medium"},
		}}
		input := registry.Model{Name: "sonnet", Fallbacks: registry.ModelList{{Name: "sol"}}}
		result, err := ApplyDefaults(DefaultOptions{Model: input, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Mode).To(Equal(registry.ModeAgent))
		Expect(result.Model.Effort).To(Equal(registry.EffortHigh))
		Expect(result.Model.Fallbacks[0].Mode).To(Equal(registry.ModeAPI))
		Expect(result.Model.Fallbacks[0].Effort).To(Equal(registry.EffortMedium))
		Expect(result.Sources).To(Equal(map[string]string{
			"/mode": "ai.providers.anthropic.mode", "/effort": "ai.providers.anthropic.reasoningEffort",
			"/fallbacks/0/mode": "ai.providers.openai.mode", "/fallbacks/0/effort": "ai.providers.openai.reasoningEffort",
		}))
		Expect(input.Fallbacks[0].Mode).To(BeEmpty())
		Expect(result.Unconfigured).To(BeEmpty())
	})

	It("resolves an agent-named provider block and retains its authored source key", func() {
		saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
			"gemini": {Mode: "cli", Model: "gemini-3.5-flash"},
		}}
		result, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "gemini-3.5-flash"}, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Mode).To(Equal(registry.ModeCLI))
		Expect(result.Sources).To(HaveKeyWithValue("/mode", "ai.providers.gemini.mode"))
	})

	It("rejects two saved keys that name the same provider", func() {
		saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{
			"google": {Mode: "api"},
			"gemini": {Mode: "cli"},
		}}
		_, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "gemini-3.5-flash"}, Saved: saved})
		Expect(err).To(MatchError(And(ContainSubstring("ai.providers.gemini"), ContainSubstring("ai.providers.google"), ContainSubstring("same provider"))))
	})

	It("preserves the global compact selector's effort and fallback chain", func() {
		saved := captainconfig.AIDefaults{DefaultModel: "agent:sonnet:high, api:sol:medium"}
		result, err := ApplyDefaults(DefaultOptions{Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Name).To(Equal("sonnet"))
		Expect(result.Model.Effort).To(Equal(registry.EffortHigh))
		Expect(result.Model.Fallbacks).To(HaveLen(1))
		Expect(result.Model.Fallbacks[0].Mode).To(Equal(registry.ModeAPI))
		Expect(result.Model.Fallbacks[0].Effort).To(Equal(registry.EffortMedium))
		Expect(result.Sources).To(HaveKeyWithValue("/fallbacks", "ai.defaultModel"))
		Expect(result.Sources).To(HaveKeyWithValue("/fallbacks/0/effort", "ai.defaultModel"))
	})

	It("reports every missing mode without leaking a global selector across providers", func() {
		saved := captainconfig.AIDefaults{DefaultModel: "api:sonnet:high"}
		result, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "sol", Fallbacks: registry.ModelList{{Name: "gemini-3.5-flash"}}}, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Mode).To(BeEmpty())
		Expect(result.Model.Effort).To(BeEmpty())
		Expect(result.Model.Fallbacks[0].Mode).To(BeEmpty())
		Expect(result.Unconfigured).To(Equal([]UnconfiguredCandidate{
			{Path: "/mode", Model: "sol", Provider: registry.OpenAI},
			{Path: "/fallbacks/0/mode", Model: "gemini-3.5-flash", Provider: registry.Google},
		}))
	})

	It("does not load any model when no selection or saved configuration exists", func() {
		result, err := ApplyDefaults(DefaultOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Name).To(BeEmpty())
		Expect(result.Sources).To(BeEmpty())
	})

	It("preserves explicit false, zero and empty fallbacks over saved settings", func() {
		zero := 0.0
		model := (registry.Model{Name: "sonnet", Temperature: &zero, Fallbacks: registry.ModelList{}}).WithExplicit("/noCache", "/fallbacks")
		saved := captainconfig.AIDefaults{DefaultModel: "agent:sonnet:high,api:sol", Temperature: 0.8, NoCache: true}
		result, err := ApplyDefaults(DefaultOptions{Model: model, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.NoCache).To(BeFalse())
		Expect(*result.Model.Temperature).To(BeZero())
		Expect(result.Model.Fallbacks).To(BeEmpty())
		Expect(result.Sources).NotTo(HaveKey("/noCache"))
		Expect(result.Sources).NotTo(HaveKey("/temperature"))
		Expect(result.Sources).NotTo(HaveKey("/fallbacks"))
	})

	It("applies an explicitly saved zero temperature and false cache policy", func() {
		saved := (captainconfig.AIDefaults{}).WithExplicit("/temperature", "/noCache")
		result, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "agent:sonnet"}, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Temperature).NotTo(BeNil())
		Expect(*result.Model.Temperature).To(BeZero())
		Expect(result.Sources).To(HaveKeyWithValue("/temperature", "ai.temperature"))
		Expect(result.Sources).To(HaveKeyWithValue("/noCache", "ai.noCache"))
	})

	It("keeps same-valued explicit choices out of saved provenance", func() {
		saved := captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{"anthropic": {Mode: "agent", ReasoningEffort: "high"}}}
		result, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "agent:sonnet:high"}, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Mode).To(Equal(registry.ModeAgent))
		Expect(result.Model.Effort).To(Equal(registry.EffortHigh))
		Expect(result.Sources).To(BeEmpty())
	})

	DescribeTable("applies only declared catalog effort defaults when enabled", func(enabled bool, expected registry.Effort) {
		result, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "agent:fable", Fallbacks: registry.ModelList{{Name: "api:fable"}}}, CatalogDefaults: enabled})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Name).To(Equal("fable"))
		Expect(result.Model.Effort).To(Equal(expected))
		Expect(result.Model.Fallbacks[0].Effort).To(Equal(expected))
		if enabled {
			Expect(result.Sources).To(HaveKeyWithValue("/effort", "registry.models.claude-fable-5-1.defaultEffort"))
			Expect(result.Sources).To(HaveKeyWithValue("/fallbacks/0/effort", "registry.models.claude-fable-5-1.defaultEffort"))
		}
	}, Entry("saved defaults enabled", true, registry.EffortHigh), Entry("catalog/replay projection", false, registry.EffortNone))

	It("keeps an explicit empty effort over the catalog default", func() {
		result, err := ApplyDefaults(DefaultOptions{Model: (registry.Model{Name: "agent:fable"}).WithExplicit("/effort"), CatalogDefaults: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Effort).To(BeEmpty())
		Expect(result.Sources).NotTo(HaveKey("/effort"))
	})

	It("keeps explicit zero temperature at the flag projection boundary", func() {
		model, err := (ModelFlags{Temperature: "0"}).ToModel()
		Expect(err).NotTo(HaveOccurred())
		Expect(model.Temperature).NotTo(BeNil())
		Expect(*model.Temperature).To(BeZero())
	})

	DescribeTable("retains raw selectors when an explicit mode is validated across flag fallbacks", func(primary string) {
		model, err := (ModelFlags{Model: primary, Mode: "api", Fallback: []string{"api:sol:medium"}}).ToModel()
		Expect(err).NotTo(HaveOccurred())
		Expect(model.Name).To(Equal(primary))
		Expect(model.Fallbacks[0].Name).To(Equal("api:sol:medium"))
	}, Entry("compact primary", "api:sonnet:high"), Entry("bare primary", "sonnet"))

	It("does not replace an explicitly false cache flag with a saved true", func() {
		flags := (ModelFlags{Model: "agent:sonnet"}).WithExplicit("/noCache")
		model, err := flags.ResolveWith(captainconfig.AIDefaults{NoCache: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(model.NoCache).To(BeFalse())
	})

	It("rejects a separate mode that contradicts the compact selector", func() {
		_, err := (ModelFlags{Model: "agent:sonnet", Mode: "api"}).ToModel()
		Expect(err).To(MatchError(ContainSubstring("contradicts requested mode")))
	})

	It("inherits authored primary knobs before provider defaults without inheriting its mode", func() {
		temperature := 0.2
		model := (registry.Model{Name: "api:sonnet:high", Temperature: &temperature, Fallbacks: registry.ModelList{{Name: "haiku"}, {Name: "sol"}}}).WithExplicit("/noCache")
		saved := captainconfig.AIDefaults{Temperature: 0.8, NoCache: true, Providers: map[string]captainconfig.ProviderDefaults{
			"anthropic": {Mode: "agent", ReasoningEffort: "low"}, "openai": {Mode: "api", ReasoningEffort: "medium"},
		}}
		result, err := ApplyDefaults(DefaultOptions{Model: model, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Fallbacks[0].Effort).To(Equal(registry.EffortHigh))
		Expect(result.Model.Fallbacks[0].Mode).To(Equal(registry.ModeAgent))
		Expect(result.Model.Fallbacks[1].Effort).To(Equal(registry.EffortMedium))
		for i, fallback := range result.Model.Fallbacks {
			Expect(*fallback.Temperature).To(Equal(temperature))
			Expect(fallback.NoCache).To(BeFalse())
			Expect(result.Sources).To(HaveKeyWithValue(fmt.Sprintf("/fallbacks/%d/temperature", i), "primary.temperature"))
			Expect(result.Sources).To(HaveKeyWithValue(fmt.Sprintf("/fallbacks/%d/noCache", i), "primary.noCache"))
		}
		Expect(result.Sources).To(HaveKeyWithValue("/fallbacks/0/effort", "primary.effort"))
	})

	DescribeTable("rejects malformed saved values even when authored fields would replace them", func(saved captainconfig.AIDefaults, source string) {
		temperature := 0.2
		_, err := ApplyDefaults(DefaultOptions{Model: registry.Model{Name: "agent:sonnet:high", Temperature: &temperature}, Saved: saved})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(source))
	},
		Entry("temperature", captainconfig.AIDefaults{Temperature: 3}, "ai.temperature"),
		Entry("cost", captainconfig.AIDefaults{BudgetUSD: -1}, "ai.budgetUSD"),
		Entry("token ceiling", captainconfig.AIDefaults{MaxTokens: -1}, "ai.maxTokens"),
		Entry("timeout", captainconfig.AIDefaults{Timeout: "tomorrow"}, "ai.timeout"),
		Entry("unknown global model", captainconfig.AIDefaults{DefaultModel: "unknown-example"}, "ai.defaultModel"),
		Entry("provider mode", captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{"openai": {Mode: "invalid"}}}, "ai.providers.openai.mode"),
		Entry("provider effort", captainconfig.AIDefaults{Providers: map[string]captainconfig.ProviderDefaults{"openai": {ReasoningEffort: "invalid"}}}, "ai.providers.openai.reasoningEffort"),
	)
})
