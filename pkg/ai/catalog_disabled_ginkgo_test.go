package ai

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

// The catalog is projected from the registry at package init, long before
// captainconfig installs the opt-out set. These specs pin the consequence: every
// projection has to consult Disabled() when it is read, never when it is built.
var _ = Describe("catalog opt-out filtering", func() {
	// Install the opt-out set for one spec only. Package-level state, so the
	// zero value has to go back regardless of how the spec ends.
	disable := func(modes, providers []string, runtimes []Runtime, models, efforts []string) {
		SetDisabled(api.NewDisabledSet(modes, providers, runtimes, models, efforts))
		DeferCleanup(func() { SetDisabled(DisabledSet{}) })
	}

	runtimesIn := func(models []Model) []Runtime {
		out := make([]Runtime, 0, len(models))
		for _, m := range models {
			out = append(out, RuntimeOf(m.Provider, m.Mode))
		}
		return out
	}

	It("drops a disabled provider's models after the catalog is already built", func() {
		Expect(runtimesIn(Catalog())).To(ContainElement(RuntimeOf(DeepSeek, ModeAPI)))

		disable(nil, []string{"deepseek"}, nil, nil, nil)

		Expect(runtimesIn(Catalog())).NotTo(ContainElement(RuntimeOf(DeepSeek, ModeAPI)))
	})

	It("drops a disabled runtime mode", func() {
		disable([]string{"agent"}, nil, nil, nil, nil)

		Expect(runtimesIn(Catalog())).NotTo(ContainElements(RuntimeOf(Anthropic, ModeAgent), RuntimeOf(OpenAI, ModeAgent)))
	})

	It("drops a disabled runtime but keeps its provider's other modes", func() {
		disable(nil, nil, []Runtime{RuntimeOf(Anthropic, ModeAgent)}, nil, nil)

		runtimes := runtimesIn(Catalog())
		Expect(runtimes).NotTo(ContainElement(RuntimeOf(Anthropic, ModeAgent)))
		Expect(runtimes).To(ContainElement(RuntimeOf(Anthropic, ModeAPI)))
	})

	It("drops a bare model id from every backend that carries it", func() {
		before := modelIDsFrom(Catalog())
		Expect(before).To(ContainElement(DefaultModelID))

		disable(nil, nil, nil, []string{defaultCatalogModelID}, nil)

		// The id is provider-prefixed on the api mode and bare on the local ones;
		// a bare opt-out entry has to reach both.
		Expect(modelIDsFrom(Catalog())).NotTo(ContainElements(DefaultModelID, defaultCatalogModelID))
	})

	It("keeps a model whose opt-out entry qualifies it with another provider", func() {
		disable(nil, nil, nil, []string{"openai/" + defaultCatalogModelID}, nil)

		// The qualifier is a provider key, so an anthropic model survives an
		// openai-qualified entry naming the same id.
		ids := modelIDsFrom(Catalog())
		Expect(ids).To(ContainElements(DefaultModelID, defaultCatalogModelID))
	})

	It("drops a disabled effort tier from every row that supports it", func() {
		efforts := func() []api.Effort {
			for _, m := range Catalog() {
				if m.ID == DefaultModelID {
					return m.SupportedEfforts
				}
			}
			return nil
		}
		Expect(efforts()).To(ContainElement(api.EffortHigh))

		disable(nil, nil, nil, nil, []string{string(api.EffortHigh)})

		Expect(efforts()).NotTo(ContainElement(api.EffortHigh))
	})

	// No registry row carries a defaultEffort today, so the degradation is only
	// reachable through the live probe. Assert on the helper both projections
	// share rather than on a catalog row that cannot exercise it.
	It("degrades a default effort that the user disabled", func() {
		disable(nil, nil, nil, nil, []string{string(api.EffortHigh)})

		supported, defaultEffort := enabledEfforts(
			[]api.Effort{api.EffortLow, api.EffortHigh, api.EffortMax},
			api.EffortHigh,
		)

		Expect(supported).To(Equal([]api.Effort{api.EffortLow, api.EffortMax}))
		Expect(defaultEffort).To(Equal(api.EffortNone))
	})

	It("hides a disabled model from the served menu", func() {
		disable(nil, []string{"deepseek"}, nil, nil, nil)

		for _, info := range CatalogInfo([]string{"deepseek"}) {
			Expect(info.Provider).NotTo(Equal("deepseek"))
		}
	})

	It("retains disabled models with remediation in the live descriptive menu", func() {
		previousProbe, previousAuthProbe := adapterProbe, adapterAuthProbe
		previousCache, previousAt, previousFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
		DeferCleanup(func() {
			adapterProbe, adapterAuthProbe = previousProbe, previousAuthProbe
			adapterCache, adapterCacheAt, adapterCacheFingerprint = previousCache, previousAt, previousFingerprint
		})
		adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""
		home := GinkgoT().TempDir()
		adapterAuthProbe = func() AuthProbe { return fakeProbe(nil, nil, nil, home) }
		adapterProbe = func(AuthProbe) ([]AdapterStatus, error) { return nil, nil }
		disable(nil, []string{"deepseek"}, nil, nil, nil)

		infos, err := LiveCatalogInfo(nil)
		Expect(err).NotTo(HaveOccurred())
		var deepseek []ModelInfo
		for _, info := range infos {
			if info.Provider == "deepseek" {
				deepseek = append(deepseek, info)
			}
		}
		Expect(deepseek).NotTo(BeEmpty())
		for _, info := range deepseek {
			Expect(info.Configured).To(BeFalse())
			Expect(info.Availability.State).To(Equal(api.AvailabilityDisabled))
			Expect(info.Availability.Remediation).NotTo(BeEmpty())
		}
	})

	// The menu names the default so no client hardcodes a model id. Disabling
	// that model has to leave the menu with none marked rather than pointing a
	// picker at a row the user switched off.
	It("stops marking a default the user disabled", func() {
		defaults := func() []string {
			out := []string{}
			for _, info := range CatalogInfo(nil) {
				if info.Default {
					out = append(out, info.ID)
				}
			}
			return out
		}
		Expect(defaults()).To(Equal([]string{DefaultModelID}))

		disable(nil, nil, nil, []string{defaultCatalogModelID}, nil)

		Expect(defaults()).To(BeEmpty())
	})

	It("serves the exact Captain runtime selection for every model row", func() {
		models := Catalog()
		info := CatalogInfo(nil)

		Expect(info).To(HaveLen(len(models)))
		for index, model := range models {
			want, err := api.Model{
				Name:     model.BareID(),
				Provider: model.Provider,
				Mode:     model.Mode,
			}.WithCapabilities()
			Expect(err).NotTo(HaveOccurred())
			if model.ID != want.Name {
				want.ID = model.ID
			}
			Expect(info[index].Runtime).To(Equal(want))
		}
	})
})
