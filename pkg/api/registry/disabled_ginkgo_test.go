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

var _ = Describe("DisabledSet", func() {
	It("disables nothing when empty", func() {
		var set DisabledSet
		Expect(set.Empty()).To(BeTrue())
		Expect(set.Backend(BackendClaudeCmux)).To(BeFalse())
		Expect(set.Model(BackendGemini, "gemini-3.5-flash")).To(BeFalse())
		Expect(set.Effort(EffortUltra)).To(BeFalse())
		Expect(set.Efforts(AllEfforts())).To(Equal(AllEfforts()))
		Expect(set.Reason(BackendClaudeCmux)).To(BeEmpty())
	})

	It("normalizes case and surrounding whitespace on every axis", func() {
		set := NewDisabledSet([]string{" CMUX "}, []string{"OpenAI"}, nil, []string{" Gemini/Veo-3 "}, []string{" ULTRA"})

		Expect(set.Mode(ModeCmux)).To(BeTrue())
		Expect(set.Provider(BackendOpenAI)).To(BeTrue())
		Expect(set.Model(BackendGemini, "veo-3")).To(BeTrue())
		Expect(set.Model(BackendGeminiCLI, "veo-3")).To(BeFalse())
		Expect(set.Effort(EffortUltra)).To(BeTrue())
	})

	It("drops blank tokens rather than disabling the empty string", func() {
		set := NewDisabledSet(nil, nil, []string{"  ", ""}, nil, nil)

		Expect(set.Empty()).To(BeTrue())
	})

	DescribeTable("Backend is disabled through any of its axes",
		func(set DisabledSet, backend Backend, want bool) {
			Expect(set.Backend(backend)).To(Equal(want))
		},
		Entry("named directly", NewDisabledSet(nil, nil, []string{"claude-cmux"}, nil, nil), BackendClaudeCmux, true),
		Entry("named directly leaves its siblings alone", NewDisabledSet(nil, nil, []string{"claude-cmux"}, nil, nil), BackendCodexCmux, false),
		Entry("via its runtime mode", NewDisabledSet([]string{"cmux"}, nil, nil, nil, nil), BackendCodexCmux, true),
		Entry("via its provider family", NewDisabledSet(nil, []string{"anthropic"}, nil, nil, nil), BackendClaudeCLI, true),
		Entry("provider resolves through the backend, not its literal name", NewDisabledSet(nil, []string{"anthropic"}, nil, nil, nil), BackendAnthropic, true),
		Entry("an unrelated provider does not reach it", NewDisabledSet(nil, []string{"openai"}, nil, nil, nil), BackendClaudeCLI, false),
	)

	DescribeTable("Reason names the axis that switched a backend off",
		func(set DisabledSet, backend Backend, want string) {
			Expect(set.Reason(backend)).To(Equal(want))
		},
		Entry("its own entry wins over its mode", NewDisabledSet([]string{"cmux"}, nil, []string{"claude-cmux"}, nil, nil), BackendClaudeCmux, "backend claude-cmux"),
		Entry("mode wins over provider", NewDisabledSet([]string{"cmux"}, []string{"anthropic"}, nil, nil, nil), BackendClaudeCmux, "mode cmux"),
		Entry("provider is the last resort", NewDisabledSet(nil, []string{"anthropic"}, nil, nil, nil), BackendClaudeAgent, "provider anthropic"),
		Entry("enabled backends have no reason", NewDisabledSet(nil, []string{"openai"}, nil, nil, nil), BackendClaudeAgent, ""),
	)

	DescribeTable("Model matches qualified and bare ids",
		func(models []string, backend Backend, id string, want bool) {
			Expect(NewDisabledSet(nil, nil, nil, models, nil).Model(backend, id)).To(Equal(want))
		},
		Entry("qualified entry hits its own backend", []string{"gemini/veo-3"}, BackendGemini, "veo-3", true),
		Entry("qualified entry spares the same model elsewhere", []string{"gemini/veo-3"}, BackendGeminiCLI, "veo-3", false),
		Entry("bare entry hits every backend", []string{"veo-3"}, BackendGeminiCLI, "veo-3", true),
		Entry("a different model is untouched", []string{"veo-3"}, BackendGemini, "gemini-3.5-flash", false),
	)

	It("reports every model of a disabled backend as disabled", func() {
		set := NewDisabledSet([]string{"cmux"}, nil, nil, nil, nil)

		Expect(set.Model(BackendClaudeCmux, "claude-opus-4-7")).To(BeTrue())
		Expect(set.Model(BackendClaudeAgent, "claude-opus-4-7")).To(BeFalse())
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
