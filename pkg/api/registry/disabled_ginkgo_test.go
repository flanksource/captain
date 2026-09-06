package registry

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRegistrySuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Registry Suite")
}

// A disabled runtime is a (provider, mode) pair throughout. It has to be: "cmux
// off for anthropic but not for openai" is exactly what a single token — the
// composite id this set used to key on — cannot say.
var _ = Describe("DisabledSet", func() {
	anthropicCmux := []Runtime{{Provider: "anthropic", Mode: ModeCmux}}

	It("disables nothing when empty", func() {
		var set DisabledSet
		Expect(set.Empty()).To(BeTrue())
		Expect(set.Runtime(Anthropic, ModeCmux)).To(BeFalse())
		Expect(set.Model(Google, ModeAPI, "gemini-3.5-flash")).To(BeFalse())
		Expect(set.Effort(EffortUltra)).To(BeFalse())
		Expect(set.Efforts(AllEfforts())).To(Equal(AllEfforts()))
		Expect(set.Reason(Anthropic, ModeCmux)).To(BeEmpty())
	})

	It("normalizes case and surrounding whitespace on every axis", func() {
		set := NewDisabledSet([]string{" CMUX "}, []string{"OpenAI"}, nil, []string{" Google/Veo-3 "}, []string{" ULTRA"})

		Expect(set.Mode(ModeCmux)).To(BeTrue())
		Expect(set.Provider(OpenAI)).To(BeTrue())
		Expect(set.Model(Google, ModeAPI, "veo-3")).To(BeTrue())
		Expect(set.Model(Anthropic, ModeAPI, "veo-3")).To(BeFalse())
		Expect(set.Effort(EffortUltra)).To(BeTrue())
	})

	It("canonicalizes provider aliases for provider and runtime lookups", func() {
		set := NewDisabledSet(nil, []string{"gemini"}, []Runtime{{Provider: "codex", Mode: ModeAgent}}, nil, nil)

		Expect(set.Provider(Google)).To(BeTrue())
		Expect(set.Runtime(OpenAI, ModeAgent)).To(BeTrue())
	})

	It("drops blank tokens rather than disabling the empty string", func() {
		set := NewDisabledSet([]string{"  ", ""}, nil, nil, nil, nil)

		Expect(set.Empty()).To(BeTrue())
	})

	DescribeTable("a runtime is disabled through any of its axes",
		func(set DisabledSet, p *Provider, mode RuntimeMode, want bool) {
			Expect(set.Runtime(p, mode)).To(Equal(want))
		},
		Entry("named directly", NewDisabledSet(nil, nil, anthropicCmux, nil, nil), Anthropic, ModeCmux, true),
		Entry("the pair leaves the same mode on another provider alone",
			NewDisabledSet(nil, nil, anthropicCmux, nil, nil), OpenAI, ModeCmux, false),
		Entry("the pair leaves another mode on the same provider alone",
			NewDisabledSet(nil, nil, anthropicCmux, nil, nil), Anthropic, ModeAgent, false),
		Entry("via its runtime mode", NewDisabledSet([]string{"cmux"}, nil, nil, nil, nil), OpenAI, ModeCmux, true),
		Entry("via its provider family", NewDisabledSet(nil, []string{"anthropic"}, nil, nil, nil), Anthropic, ModeCLI, true),
		Entry("an unrelated provider does not reach it", NewDisabledSet(nil, []string{"openai"}, nil, nil, nil), Anthropic, ModeCLI, false),
	)

	DescribeTable("Reason names the axis that switched a runtime off, in both halves",
		func(set DisabledSet, p *Provider, mode RuntimeMode, want string) {
			Expect(set.Reason(p, mode)).To(Equal(want))
		},
		Entry("its own entry wins over its mode",
			NewDisabledSet([]string{"cmux"}, nil, anthropicCmux, nil, nil), Anthropic, ModeCmux, "runtime anthropic cmux"),
		Entry("mode wins over provider",
			NewDisabledSet([]string{"cmux"}, []string{"anthropic"}, nil, nil, nil), Anthropic, ModeCmux, "mode cmux"),
		Entry("provider is the last resort",
			NewDisabledSet(nil, []string{"anthropic"}, nil, nil, nil), Anthropic, ModeAgent, "provider anthropic"),
		Entry("enabled runtimes have no reason",
			NewDisabledSet(nil, []string{"openai"}, nil, nil, nil), Anthropic, ModeAgent, ""),
	)

	DescribeTable("Model matches provider-qualified and bare ids",
		func(models []string, p *Provider, id string, want bool) {
			Expect(NewDisabledSet(nil, nil, nil, models, nil).Model(p, ModeAPI, id)).To(Equal(want))
		},
		Entry("qualified entry hits its own provider", []string{"google/veo-3"}, Google, "veo-3", true),
		Entry("qualified entry spares the same model elsewhere", []string{"google/veo-3"}, Anthropic, "veo-3", false),
		Entry("bare entry hits every provider", []string{"veo-3"}, Anthropic, "veo-3", true),
		Entry("a different model is untouched", []string{"veo-3"}, Google, "gemini-3.5-flash", false),
	)

	It("reports every model of a disabled runtime as disabled", func() {
		set := NewDisabledSet([]string{"cmux"}, nil, nil, nil, nil)

		Expect(set.Model(Anthropic, ModeCmux, "claude-opus-4-7")).To(BeTrue())
		Expect(set.Model(Anthropic, ModeAgent, "claude-opus-4-7")).To(BeFalse())
	})

	It("filters an effort list in place, keeping the caller's order", func() {
		set := NewDisabledSet(nil, nil, nil, nil, []string{"medium", "ultra"})

		Expect(set.Efforts([]Effort{EffortUltra, EffortLow, EffortMedium, EffortHigh})).
			To(Equal([]Effort{EffortLow, EffortHigh}))
		Expect(set.EnabledEfforts()).To(Equal([]Effort{EffortLow, EffortHigh, EffortXHigh, EffortMax}))
	})

	It("returns nil once every supplied tier is disabled", func() {
		set := NewDisabledSet(nil, nil, nil, nil, []string{"low", "medium"})

		Expect(set.Efforts([]Effort{EffortLow, EffortMedium})).To(BeNil())
	})

	It("never disables EffortNone, which names no tier", func() {
		set := NewDisabledSet(nil, nil, nil, nil, []string{"low", "medium", "high", "xhigh", "max", "ultra"})

		Expect(set.Effort(EffortNone)).To(BeFalse())
		Expect(set.EnabledEfforts()).To(BeEmpty())
	})

	It("installs process-wide and reads back", func() {
		DeferCleanup(func() { SetDisabled(DisabledSet{}) })
		SetDisabled(NewDisabledSet([]string{"cmux"}, nil, nil, nil, nil))

		Expect(Disabled().Mode(ModeCmux)).To(BeTrue())
	})
})
