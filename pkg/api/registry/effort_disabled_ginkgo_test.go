package registry

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// catalogModel is a model the embedded catalog knows on BackendAnthropic, so the
// specs below exercise the "known" branch of ResolveEffort rather than the
// permissive unknown-model one.
const catalogModel = "claude-opus-5"

var _ = Describe("effort resolution under an opt-out set", func() {
	disable := func(efforts ...string) {
		SetDisabled(NewDisabledSet(nil, nil, nil, nil, efforts))
		DeferCleanup(func() { SetDisabled(DisabledSet{}) })
	}

	It("leaves a known model's tiers alone when nothing is disabled", func() {
		supported, _, ok := ModelEfforts(BackendAnthropic, catalogModel)

		Expect(ok).To(BeTrue())
		Expect(supported).To(ContainElement(EffortMax))
	})

	It("prunes disabled tiers from a known model's supported list", func() {
		disable("max", "xhigh")

		supported, _, ok := ModelEfforts(BackendAnthropic, catalogModel)

		Expect(ok).To(BeTrue())
		Expect(supported).NotTo(ContainElements(EffortMax, EffortXHigh))
		Expect(supported).To(ContainElement(EffortHigh))
	})

	It("degrades a disabled tier on a known model to its highest enabled one", func() {
		disable("max", "xhigh")

		Expect(ResolveEffort(BackendAnthropic, catalogModel, EffortMax)).To(Equal(EffortHigh))
	})

	It("keeps an enabled tier on a known model untouched", func() {
		disable("max")

		Expect(ResolveEffort(BackendAnthropic, catalogModel, EffortLow)).To(Equal(EffortLow))
	})

	It("errors when every tier a known model supports is disabled", func() {
		disable("low", "medium", "high", "xhigh", "max", "ultra")

		_, err := ResolveEffort(BackendAnthropic, catalogModel, EffortHigh)

		Expect(err).To(MatchError(ContainSubstring("ai.disabled.efforts")))
	})

	It("keeps an unknown model's requested tier when it is enabled", func() {
		disable("ultra")

		Expect(ResolveEffort(BackendAnthropic, "some-unreleased-model", EffortHigh)).To(Equal(EffortHigh))
	})

	It("degrades an unknown model's disabled tier to the nearest enabled one below it", func() {
		disable("high")

		Expect(ResolveEffort(BackendAnthropic, "some-unreleased-model", EffortHigh)).To(Equal(EffortMedium))
	})

	It("degrades upward when nothing is enabled below the requested tier", func() {
		disable("low", "medium")

		Expect(ResolveEffort(BackendAnthropic, "some-unreleased-model", EffortLow)).To(Equal(EffortHigh))
	})

	It("errors on an unknown model when no tier is left enabled", func() {
		disable("low", "medium", "high", "xhigh", "max", "ultra")

		_, err := ResolveEffort(BackendAnthropic, "some-unreleased-model", EffortHigh)

		Expect(err).To(MatchError(ContainSubstring("no tier is left enabled")))
	})

	It("never degrades EffortNone, which asks for the backend default", func() {
		disable("low", "medium", "high", "xhigh", "max", "ultra")

		Expect(ResolveEffort(BackendAnthropic, catalogModel, EffortNone)).To(Equal(EffortNone))
	})
})
