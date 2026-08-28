package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

func runtimesOf(families []api.RuntimeFamily) []string {
	out := make([]string, 0, len(families))
	for _, f := range families {
		for _, m := range f.Modes {
			out = append(out, f.Provider+":"+m.Backend)
		}
	}
	return out
}

func familyNamed(families []api.RuntimeFamily, name string) api.RuntimeFamily {
	for _, f := range families {
		if f.Family == name {
			return f
		}
	}
	return api.RuntimeFamily{}
}

func modeNamed(f api.RuntimeFamily, mode string) api.RuntimeModeEntry {
	for _, m := range f.Modes {
		if m.Backend == mode {
			return m
		}
	}
	return api.RuntimeModeEntry{}
}

var _ = Describe("RuntimeCatalog", func() {
	disable := func(modes, providers, backends []string) {
		api.SetDisabled(api.NewDisabledSet(modes, providers, backends, nil, nil))
		DeferCleanup(func() { api.SetDisabled(api.DisabledSet{}) })
	}

	It("serves every provider and backend pair exactly once", func() {
		families := api.RuntimeCatalog()

		names := make([]string, len(families))
		for i, f := range families {
			names[i] = f.Family
		}
		Expect(names).To(ConsistOf("claude", "codex", "gemini", "deepseek"))

		expected := make([]string, 0, len(api.AllBackends()))
		for _, backend := range api.AllBackends() {
			expected = append(expected, string(backend.Provider())+":"+string(backend.Mode()))
		}
		Expect(runtimesOf(families)).To(ConsistOf(expected))
	})

	It("names the provider key and catalog prefix separately for gemini", func() {
		gemini := familyNamed(api.RuntimeCatalog(), "gemini")

		// google/googleai is the rename that every hand-written ladder got
		// wrong; serving both means a client never has to guess.
		Expect(gemini.Provider).To(Equal("google"))
		Expect(gemini.CatalogPrefix).To(Equal("googleai"))
	})

	It("orders modes canonically and reports each mode's kind", func() {
		claude := familyNamed(api.RuntimeCatalog(), "claude")

		modes := make([]string, len(claude.Modes))
		kinds := make([]string, len(claude.Modes))
		for i, m := range claude.Modes {
			modes[i], kinds[i] = m.Backend, m.Kind
		}
		Expect(modes).To(Equal([]string{"api", "agent", "cli", "cmux"}))
		Expect(kinds).To(Equal([]string{"api", "cli", "cli", "cli"}))
	})

	It("names the model-menu catalog provider of every mode", func() {
		claude := familyNamed(api.RuntimeCatalog(), "claude")
		Expect(modeNamed(claude, "api").CatalogProvider).To(Equal("anthropic"))
		Expect(modeNamed(claude, "agent").CatalogProvider).To(Equal("anthropic"))
		Expect(modeNamed(claude, "cli").CatalogProvider).To(Equal("anthropic"))
		Expect(modeNamed(claude, "cmux").CatalogProvider).To(Equal("anthropic"))

		codex := familyNamed(api.RuntimeCatalog(), "codex")
		Expect(modeNamed(codex, "api").CatalogProvider).To(Equal("openai"))
		Expect(modeNamed(codex, "agent").CatalogProvider).To(Equal("openai"))
		Expect(modeNamed(codex, "cli").CatalogProvider).To(Equal("openai"))
		Expect(modeNamed(codex, "cmux").CatalogProvider).To(Equal("openai"))
	})

	It("keeps a family with no agent mode on its catalog prefix", func() {
		// Gemini's CLI models are already listed under the googleai API rows, so
		// there is no separate agent catalog for them to collapse onto.
		gemini := familyNamed(api.RuntimeCatalog(), "gemini")
		Expect(modeNamed(gemini, "api").CatalogProvider).To(Equal("googleai"))
		Expect(modeNamed(gemini, "cli").CatalogProvider).To(Equal("googleai"))

		deepseek := familyNamed(api.RuntimeCatalog(), "deepseek")
		Expect(modeNamed(deepseek, "api").CatalogProvider).To(Equal("deepseek"))
	})

	It("marks only cmux modes keyless", func() {
		for _, f := range api.RuntimeCatalog() {
			for _, m := range f.Modes {
				Expect(m.Keyless).To(Equal(m.Backend == "cmux"), "backend %s", m.Backend)
			}
		}
	})

	It("reports nothing disabled with no opt-out set installed", func() {
		for _, f := range api.RuntimeCatalog() {
			for _, m := range f.Modes {
				Expect(m.Disabled).To(BeFalse())
				Expect(m.DisabledReason).To(BeEmpty())
				Expect(m.Availability).To(Equal(api.Available()))
			}
		}
	})

	It("annotates rather than drops a disabled mode, and names the switch", func() {
		disable([]string{"cmux"}, nil, nil)

		families := api.RuntimeCatalog()
		Expect(runtimesOf(families)).To(ContainElements("anthropic:cmux", "openai:cmux"))

		cmux := modeNamed(familyNamed(families, "claude"), "cmux")
		Expect(cmux.Disabled).To(BeTrue())
		Expect(cmux.DisabledReason).To(Equal("mode cmux"))
		Expect(cmux.Availability.State).To(Equal(api.AvailabilityDisabled))
		Expect(cmux.Availability.Reason).To(ContainSubstring("mode cmux"))
		Expect(cmux.Availability.Remediation).NotTo(BeEmpty())
		Expect(modeNamed(familyNamed(families, "claude"), "api").Disabled).To(BeFalse())
	})

	It("disables every mode of a disabled provider", func() {
		disable(nil, []string{"deepseek"}, nil)

		for _, m := range familyNamed(api.RuntimeCatalog(), "deepseek").Modes {
			Expect(m.Disabled).To(BeTrue())
			Expect(m.DisabledReason).To(Equal("provider deepseek"))
		}
	})

	It("distinguishes a directly-disabled backend from its mode and provider", func() {
		disable(nil, nil, []string{"claude-agent"})

		claude := familyNamed(api.RuntimeCatalog(), "claude")
		Expect(modeNamed(claude, "agent").DisabledReason).To(Equal("backend claude-agent"))
		Expect(modeNamed(familyNamed(api.RuntimeCatalog(), "codex"), "agent").Disabled).To(BeFalse())
	})
})
