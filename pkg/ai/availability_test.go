package ai

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("runtime availability", func() {
	DescribeTable("classifies adapter readiness",
		func(input AdapterStatus, state api.AvailabilityState, text string) {
			got := AvailabilityForAdapter(input)
			Expect(got.State).To(Equal(state))
			if text != "" {
				Expect(strings.ToLower(got.Reason)).To(ContainSubstring(strings.ToLower(text)))
			}
			if !got.IsAvailable() {
				Expect(got.Remediation).NotTo(BeEmpty())
			}
		},
		Entry("disabled mode",
			AdapterStatus{Provider: Anthropic.Name, Mode: string(ModeCmux), Type: "cli", Authenticated: true, Binary: "/bin/claude",
				Disabled: true, DisabledReason: "mode cmux"},
			api.AvailabilityDisabled, "mode cmux"),
		Entry("missing API credentials",
			AdapterStatus{Provider: OpenAI.Name, Mode: string(ModeAPI), Type: "api"},
			api.AvailabilityMissingCredential, "credentials"),
		Entry("local CLI not authenticated",
			AdapterStatus{Provider: OpenAI.Name, Mode: string(ModeCLI), Type: "cli", Binary: "/bin/codex"},
			api.AvailabilityNotAuthenticated, "authenticated"),
		Entry("missing executable",
			AdapterStatus{Provider: Google.Name, Mode: string(ModeCLI), Type: "cli", Authenticated: true, BinaryMissing: "gemini"},
			api.AvailabilityMissingExecutable, "gemini"),
		Entry("missing runtime dependency",
			AdapterStatus{Provider: Anthropic.Name, Mode: string(ModeAgent), Type: "cli", Authenticated: true, DependencyMissing: "npm"},
			api.AvailabilityMissingDependency, "npm"),
		Entry("ready",
			AdapterStatus{Provider: OpenAI.Name, Mode: string(ModeCLI), Type: "cli", Authenticated: true, Binary: "/bin/codex"},
			api.AvailabilityAvailable, ""),
	)

	It("does not expose runtime probe error details", func() {
		availability := AvailabilityForAdapter(AdapterStatus{
			Provider:     OpenAI.Name,
			Mode:         string(ModeCLI),
			Type:         "cli",
			RuntimeError: "inspect token=super-secret: permission denied",
		})

		Expect(availability.State).To(Equal(api.AvailabilityUnavailable))
		Expect(availability.Reason).To(Equal("Codex CLI prerequisites could not be inspected."))
		Expect(availability.Reason).NotTo(ContainSubstring("super-secret"))
	})

	It("uses each adapter's live readiness in the runtime catalog", func() {
		previousProbe, previousAuthProbe := adapterProbe, adapterAuthProbe
		previousCache, previousAt, previousFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
		DeferCleanup(func() {
			adapterProbe, adapterAuthProbe = previousProbe, previousAuthProbe
			adapterCache, adapterCacheAt, adapterCacheFingerprint = previousCache, previousAt, previousFingerprint
		})
		adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""
		home := GinkgoT().TempDir()
		adapterAuthProbe = func() AuthProbe { return fakeProbe(nil, nil, nil, home) }
		adapterProbe = func(AuthProbe) ([]AdapterStatus, error) {
			return []AdapterStatus{
				{Provider: OpenAI.Name, Mode: string(ModeAPI), Type: "api"},
				{Provider: OpenAI.Name, Mode: string(ModeCLI), Type: "cli", Binary: "/bin/codex"},
				{Provider: Anthropic.Name, Mode: string(ModeAgent), Type: "cli", Authenticated: true, DependencyMissing: "npm"},
			}, nil
		}

		runtimes, err := LiveRuntimeCatalog()
		Expect(err).NotTo(HaveOccurred())
		Expect(runtimeEntry(runtimes, RuntimeOf(Anthropic, ModeAgent)).CatalogProvider).To(Equal("anthropic"))
		Expect(runtimeEntry(runtimes, RuntimeOf(OpenAI, ModeAPI)).CatalogProvider).To(Equal("openai"))
		for _, test := range []struct {
			runtime Runtime
			state   api.AvailabilityState
		}{
			{runtime: RuntimeOf(OpenAI, ModeAPI), state: api.AvailabilityMissingCredential},
			{runtime: RuntimeOf(OpenAI, ModeCLI), state: api.AvailabilityNotAuthenticated},
			{runtime: RuntimeOf(Anthropic, ModeAgent), state: api.AvailabilityMissingDependency},
		} {
			availability := runtimeAvailability(runtimes, test.runtime)
			Expect(availability.State).To(Equal(test.state), test.runtime.String())
			Expect(availability.Remediation).NotTo(BeEmpty(), test.runtime.String())
		}
	})

	It("projects runtime availability from the supplied whoami adapters", func() {
		runtimes := RuntimeCatalogFromAdapters([]AdapterStatus{
			{Provider: OpenAI.Name, Mode: string(ModeAPI), Type: "api"},
			{Provider: OpenAI.Name, Mode: string(ModeCLI), Type: "cli", Authenticated: true, Binary: "/bin/codex"},
		})

		Expect(runtimeAvailability(runtimes, RuntimeOf(OpenAI, ModeAPI)).State).To(Equal(api.AvailabilityMissingCredential))
		Expect(runtimeAvailability(runtimes, RuntimeOf(OpenAI, ModeCLI)).State).To(Equal(api.AvailabilityAvailable))
	})

	It("projects picker models from the supplied whoami adapters", func() {
		models := CatalogInfoFromAdapters([]AdapterStatus{{
			Provider: OpenAI.Name, Mode: string(ModeCLI), Type: "cli",
			Authenticated: true, Binary: "/bin/codex",
			ModelDetails: []ModelDef{{
				ID: "gpt-whoami-only", Name: "GPT Whoami Only",
				CapabilitiesKnown: true, Reasoning: true,
				SupportedEfforts: []api.Effort{api.EffortLow, api.EffortHigh},
				DefaultEffort:    api.EffortHigh,
			}},
		}})

		var projected *ModelInfo
		for i := range models {
			if models[i].ID == "gpt-whoami-only" {
				projected = &models[i]
				break
			}
		}
		Expect(projected).NotTo(BeNil())
		Expect(projected.Provider).To(Equal("openai"))
		Expect(projected.Runtime.Name).To(Equal("gpt-whoami-only"))
		Expect(projected.Configured).To(BeTrue())
		Expect(projected.Availability.State).To(Equal(api.AvailabilityAvailable))
	})

	It("keeps Gemini CLI models separate from the unconfigured API catalog", func() {
		models := CatalogInfoFromAdapters([]AdapterStatus{
			{Provider: Google.Name, Mode: string(ModeAPI), Type: "api"},
			{
				Provider: Google.Name, Mode: string(ModeCLI), Type: "cli",
				Authenticated: true, Binary: "/bin/gemini",
				ModelDetails: []ModelDef{{ID: "gemini-cli-live", Name: "Gemini CLI Live"}},
			},
		})

		var cli *ModelInfo
		for i := range models {
			if models[i].ID == "gemini-cli-live" {
				cli = &models[i]
				break
			}
		}
		Expect(cli).NotTo(BeNil())
		Expect(cli.Provider).To(Equal("google"))
		Expect(cli.Runtime.Mode).To(Equal(ModeCLI))
		Expect(cli.Configured).To(BeTrue())
		Expect(cli.Availability.State).To(Equal(api.AvailabilityAvailable))
	})
})

func runtimeAvailability(families []api.RuntimeFamily, runtime Runtime) api.Availability {
	return runtimeEntry(families, runtime).Availability
}

// runtimeEntry matches on the two axes independently: the family carries the
// provider key, the entry carries the mode. The version this replaces compared a
// single composite adapter id, which the catalog no longer emits — so every
// lookup missed and every runtime reported "readiness was not reported".
func runtimeEntry(families []api.RuntimeFamily, runtime Runtime) api.RuntimeModeEntry {
	for _, family := range families {
		if family.Provider != runtime.Provider {
			continue
		}
		for _, mode := range family.Modes {
			if mode.Mode == string(runtime.Mode) {
				return mode
			}
		}
	}
	Fail("runtime " + runtime.String() + " not found")
	return api.RuntimeModeEntry{}
}
