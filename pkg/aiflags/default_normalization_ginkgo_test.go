package aiflags

import (
	"errors"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Saved model selection normalization", func() {
	DescribeTable("inherits authored primary effort before saved fallback effort", func(fallbacks registry.ModelList, effort registry.Effort, inherited bool) {
		result, err := ApplyDefaults(DefaultOptions{
			Model: registry.Model{Name: "sonnet", Effort: registry.EffortHigh, Fallbacks: fallbacks},
			Saved: captainconfig.AIDefaults{DefaultModel: "api:sonnet,api:haiku:low"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Fallbacks).To(HaveLen(1))
		Expect(result.Model.Fallbacks[0].Effort).To(Equal(effort))
		if inherited {
			Expect(result.Sources).To(HaveKeyWithValue("/fallbacks/0/effort", "primary.effort"))
		} else {
			Expect(result.Sources).NotTo(HaveKey("/fallbacks/0/effort"))
		}
	},
		Entry("saved fallback knob remains a gap", nil, registry.EffortHigh, true),
		Entry("authored fallback effort wins", registry.ModelList{{Name: "api:haiku:low"}}, registry.EffortLow, false),
		Entry("authored empty fallback effort wins", registry.ModelList{(registry.Model{Name: "api:haiku"}).WithExplicit("/effort")}, registry.EffortNone, false),
	)

	It("normalizes the complete saved candidate chain once before provider defaults", func() {
		saved := captainconfig.AIDefaults{
			DefaultModel: "sonnet,sol", Temperature: 0.3,
			Providers: map[string]captainconfig.ProviderDefaults{
				"anthropic": {Mode: "api", ReasoningEffort: "high"},
				"openai":    {Mode: "api", ReasoningEffort: "low"},
			},
		}
		calls := 0
		result, err := ApplyDefaults(DefaultOptions{Saved: saved, Normalize: func(model registry.Model) (registry.Model, error) {
			calls++
			Expect(model.Name).To(Equal("sonnet"))
			Expect(model.Mode).To(BeEmpty())
			Expect(model.Effort).To(BeEmpty())
			Expect(model.Temperature).To(BeNil())
			Expect(model.Fallbacks).To(HaveLen(1))
			Expect(model.Fallbacks[0].Name).To(Equal("sol"))
			Expect(model.Fallbacks[0].Mode).To(BeEmpty())
			Expect(model.Fallbacks[0].Effort).To(BeEmpty())
			model.Mode = registry.ModeCLI
			model.Fallbacks[0].Mode = registry.ModeCLI
			return model, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))
		Expect(result.Model.Mode).To(Equal(registry.ModeCLI))
		Expect(result.Model.Fallbacks[0].Mode).To(Equal(registry.ModeCLI))
		Expect(result.Model.Effort).To(Equal(registry.EffortHigh))
		Expect(result.Model.Fallbacks[0].Effort).To(Equal(registry.EffortLow))
		Expect(result.Sources).To(HaveKeyWithValue("/model", "ai.defaultModel"))
		Expect(result.Sources).To(HaveKeyWithValue("/fallbacks/0/model", "ai.defaultModel"))
		Expect(result.Sources).NotTo(HaveKey("/mode"))
		Expect(result.Sources).NotTo(HaveKey("/fallbacks/0/mode"))
	})

	It("keeps explicit empty fallbacks and passes authored compact pins to normalization", func() {
		model := (registry.Model{Name: "api:sonnet:high", Fallbacks: registry.ModelList{}}).WithExplicit("/fallbacks")
		calls := 0
		result, err := ApplyDefaults(DefaultOptions{
			Model: model, Saved: captainconfig.AIDefaults{DefaultModel: "sonnet,sol"},
			Normalize: func(selected registry.Model) (registry.Model, error) {
				calls++
				Expect(selected.Name).To(Equal("sonnet"))
				Expect(selected.Mode).To(Equal(registry.ModeAPI))
				Expect(selected.Effort).To(Equal(registry.EffortHigh))
				Expect(selected.Fallbacks).To(BeEmpty())
				return selected, nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))
		Expect(result.Model.Fallbacks).To(BeEmpty())
		Expect(model.Name).To(Equal("api:sonnet:high"))
	})

	It("treats saved compact modes as gaps on both primary and fallback", func() {
		result, err := ApplyDefaults(DefaultOptions{
			Saved: captainconfig.AIDefaults{DefaultModel: "api:sonnet:high,api:sol:medium"},
			Normalize: func(model registry.Model) (registry.Model, error) {
				Expect(model.Mode).To(BeEmpty())
				Expect(model.Fallbacks).To(HaveLen(1))
				Expect(model.Fallbacks[0].Mode).To(BeEmpty())
				model.Mode = registry.ModeCLI
				model.Fallbacks[0].Mode = registry.ModeCLI
				return model, nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Model.Mode).To(Equal(registry.ModeCLI))
		Expect(result.Model.Fallbacks[0].Mode).To(Equal(registry.ModeCLI))
		Expect(result.Model.Effort).To(Equal(registry.EffortHigh))
		Expect(result.Model.Fallbacks[0].Effort).To(Equal(registry.EffortMedium))
		Expect(result.Sources).NotTo(HaveKey("/mode"))
		Expect(result.Sources).NotTo(HaveKey("/fallbacks/0/mode"))
		Expect(result.Sources).To(HaveKeyWithValue("/fallbacks/0/effort", "ai.defaultModel"))
	})

	It("propagates normalization errors before adding candidate defaults", func() {
		failure := errors.New("sandbox rejects selected runtime")
		_, err := ApplyDefaults(DefaultOptions{
			Saved:     captainconfig.AIDefaults{DefaultModel: "sonnet,sol"},
			Normalize: func(registry.Model) (registry.Model, error) { return registry.Model{}, failure },
		})
		Expect(err).To(MatchError(failure))
	})

	It("rejects malformed saved values before invoking normalization", func() {
		calls := 0
		_, err := ApplyDefaults(DefaultOptions{
			Saved:     captainconfig.AIDefaults{DefaultModel: "sonnet,sol", Temperature: 3},
			Normalize: func(model registry.Model) (registry.Model, error) { calls++; return model, nil },
		})
		Expect(err).To(MatchError(ContainSubstring("ai.temperature")))
		Expect(calls).To(BeZero())
	})
})
