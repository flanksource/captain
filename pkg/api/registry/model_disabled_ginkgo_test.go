package registry

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("fallback chains under an opt-out set", func() {
	install := func(set DisabledSet) {
		SetDisabled(set)
		DeferCleanup(func() { SetDisabled(DisabledSet{}) })
	}

	// Candidates filters a chain that has already been resolved — that is the
	// only state it sees in production, because every boundary resolves before
	// building an adapter. An unresolved candidate carries no provider, so there
	// is nothing for the opt-out set to match against.
	chain := func() Model {
		resolved, err := ResolveModel(Model{
			Name:      "claude-sonnet-5",
			Fallbacks: []Model{{Name: "gpt-5.6-sol"}, {Name: "claude-haiku-4-5"}},
		})
		Expect(err).NotTo(HaveOccurred())
		return resolved
	}
	names := func(models []Model) []string {
		out := make([]string, 0, len(models))
		for _, model := range models {
			out = append(out, model.Name)
		}
		return out
	}

	It("keeps the whole chain when nothing is disabled", func() {
		Expect(names(chain().Candidates())).To(Equal([]string{"claude-sonnet-5", "gpt-5.6-sol", "claude-haiku-4-5"}))
	})

	It("drops the candidates whose provider is disabled", func() {
		install(NewDisabledSet(nil, []string{"openai"}, nil, nil, nil))

		Expect(names(chain().Candidates())).To(Equal([]string{"claude-sonnet-5", "claude-haiku-4-5"}))
	})

	It("drops a single model named on its own provider", func() {
		install(NewDisabledSet(nil, nil, nil, []string{"anthropic/claude-sonnet-5"}, nil))

		Expect(names(chain().Candidates())).To(Equal([]string{"gpt-5.6-sol", "claude-haiku-4-5"}))
	})

	It("substitutes an enabled model when the whole chain is disabled", func() {
		install(NewDisabledSet(nil, []string{"anthropic", "openai"}, nil, nil, nil))

		got := chain().Candidates()

		Expect(got).To(HaveLen(1))
		Expect(got[0].Provider).NotTo(BeNil())
		Expect(got[0].Provider.Name).NotTo(BeElementOf("anthropic", "openai"))
		Expect(got[0].Name).NotTo(BeEmpty())
	})

	It("returns the unfiltered chain when every provider is disabled, so the caller reports the dead end", func() {
		providers := make([]string, 0, len(Providers()))
		for _, p := range Providers() {
			providers = append(providers, p.Name)
		}
		install(NewDisabledSet(nil, providers, nil, nil, nil))

		Expect(names(chain().Candidates())).To(Equal([]string{"claude-sonnet-5", "gpt-5.6-sol", "claude-haiku-4-5"}))
	})

	It("keeps a candidate it cannot map to a provider rather than silently dropping it", func() {
		install(NewDisabledSet(nil, []string{"anthropic"}, nil, nil, nil))

		// Hand-built rather than resolved: an unresolvable name never survives
		// ResolveModel, so this is the shape a caller reaches Candidates with
		// only when it built the chain itself. Dropping the row here would turn
		// "no such model" into a silent disappearance.
		resolved, err := ResolveModel(Model{Name: "claude-sonnet-5"})
		Expect(err).NotTo(HaveOccurred())
		resolved.Fallbacks = []Model{{Name: "not-a-real-model-family"}}

		Expect(names(resolved.Candidates())).To(Equal([]string{"not-a-real-model-family"}))
	})
})
