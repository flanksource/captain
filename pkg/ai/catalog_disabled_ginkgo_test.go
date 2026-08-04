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
	disable := func(modes, providers, backends, models, efforts []string) {
		SetDisabled(api.NewDisabledSet(modes, providers, backends, models, efforts))
		DeferCleanup(func() { SetDisabled(DisabledSet{}) })
	}

	backendsIn := func(models []Model) []Backend {
		out := make([]Backend, 0, len(models))
		for _, m := range models {
			out = append(out, m.Backend)
		}
		return out
	}

	It("drops a disabled provider's models after the catalog is already built", func() {
		Expect(backendsIn(Catalog())).To(ContainElement(BackendDeepSeek))

		disable(nil, []string{"deepseek"}, nil, nil, nil)

		Expect(backendsIn(Catalog())).NotTo(ContainElement(BackendDeepSeek))
	})

	It("drops a disabled runtime mode", func() {
		disable([]string{"agent"}, nil, nil, nil, nil)

		Expect(backendsIn(Catalog())).NotTo(ContainElements(BackendClaudeAgent, BackendCodexAgent))
	})

	It("drops a disabled backend but keeps its provider's other backends", func() {
		disable(nil, nil, []string{"claude-agent"}, nil, nil)

		backends := backendsIn(Catalog())
		Expect(backends).NotTo(ContainElement(BackendClaudeAgent))
		Expect(backends).To(ContainElement(BackendAnthropic))
	})

	It("drops a bare model id from every backend that carries it", func() {
		before := modelIDsFrom(Catalog())
		Expect(before).To(ContainElement(DefaultModelID))

		disable(nil, nil, nil, []string{defaultCatalogModelID}, nil)

		// The id is menu-prefixed on the API backend and bare on the agent
		// backend; a bare opt-out entry has to reach both.
		Expect(modelIDsFrom(Catalog())).NotTo(ContainElements(DefaultModelID, defaultCatalogModelID))
	})

	It("keeps a model whose opt-out entry names a different backend", func() {
		disable(nil, nil, nil, []string{"claude-agent/" + defaultCatalogModelID}, nil)

		ids := modelIDsFrom(Catalog())
		Expect(ids).To(ContainElement(DefaultModelID))
		Expect(ids).NotTo(ContainElement(defaultCatalogModelID))
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
		previousProbe := adapterProbe
		previousCache, previousAt := adapterCache, adapterCacheAt
		DeferCleanup(func() {
			adapterProbe = previousProbe
			adapterCache, adapterCacheAt = previousCache, previousAt
		})
		adapterCache, adapterCacheAt = nil, time.Time{}
		adapterProbe = func() ([]AdapterStatus, error) { return nil, nil }
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
			want := api.Model{
				Name:    model.BareID(),
				Backend: model.Backend,
			}.Capabilities()
			if model.ID != want.Name {
				want.ID = model.ID
			}
			Expect(info[index].Runtime).To(Equal(want))
		}
	})
})
