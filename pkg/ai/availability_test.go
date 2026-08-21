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
			AdapterStatus{Backend: string(BackendClaudeCmux), Type: "cli", Authenticated: true, Binary: "/bin/claude",
				Disabled: true, DisabledReason: "mode cmux"},
			api.AvailabilityDisabled, "mode cmux"),
		Entry("missing API credentials",
			AdapterStatus{Backend: string(BackendOpenAI), Type: "api"},
			api.AvailabilityMissingCredential, "credentials"),
		Entry("local CLI not authenticated",
			AdapterStatus{Backend: string(BackendCodexCLI), Type: "cli", Binary: "/bin/codex"},
			api.AvailabilityNotAuthenticated, "authenticated"),
		Entry("missing executable",
			AdapterStatus{Backend: string(BackendGeminiCLI), Type: "cli", Authenticated: true, BinaryMissing: "gemini"},
			api.AvailabilityMissingExecutable, "gemini"),
		Entry("missing runtime dependency",
			AdapterStatus{Backend: string(BackendClaudeAgent), Type: "cli", Authenticated: true, DependencyMissing: "npm"},
			api.AvailabilityMissingDependency, "npm"),
		Entry("ready",
			AdapterStatus{Backend: string(BackendCodexCLI), Type: "cli", Authenticated: true, Binary: "/bin/codex"},
			api.AvailabilityAvailable, ""),
	)

	It("does not expose runtime probe error details", func() {
		availability := AvailabilityForAdapter(AdapterStatus{
			Backend:      string(BackendCodexCLI),
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
				{Backend: string(BackendOpenAI), Type: "api"},
				{Backend: string(BackendCodexCLI), Type: "cli", Binary: "/bin/codex"},
				{Backend: string(BackendClaudeAgent), Type: "cli", Authenticated: true, DependencyMissing: "npm"},
			}, nil
		}

		runtimes, err := LiveRuntimeCatalog()
		Expect(err).NotTo(HaveOccurred())
		Expect(runtimeEntry(runtimes, BackendClaudeAgent).CatalogProvider).To(Equal("claude-agent"))
		Expect(runtimeEntry(runtimes, BackendOpenAI).CatalogProvider).To(Equal("openai"))
		for _, test := range []struct {
			backend Backend
			state   api.AvailabilityState
		}{
			{backend: BackendOpenAI, state: api.AvailabilityMissingCredential},
			{backend: BackendCodexCLI, state: api.AvailabilityNotAuthenticated},
			{backend: BackendClaudeAgent, state: api.AvailabilityMissingDependency},
		} {
			availability := runtimeAvailability(runtimes, test.backend)
			Expect(availability.State).To(Equal(test.state), string(test.backend))
			Expect(availability.Remediation).NotTo(BeEmpty(), string(test.backend))
		}
	})
})

func runtimeAvailability(families []api.RuntimeFamily, backend Backend) api.Availability {
	return runtimeEntry(families, backend).Availability
}

func runtimeEntry(families []api.RuntimeFamily, backend Backend) api.RuntimeModeEntry {
	for _, family := range families {
		for _, mode := range family.Modes {
			if mode.Backend == string(backend) {
				return mode
			}
		}
	}
	Fail("runtime backend " + string(backend) + " not found")
	return api.RuntimeModeEntry{}
}
