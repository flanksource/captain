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

	chain := Model{
		Name:      "claude-sonnet-5",
		Fallbacks: []Model{{Name: "gpt-5.6-sol"}, {Name: "claude-haiku-4-5"}},
	}
	names := func(models []Model) []string {
		out := make([]string, 0, len(models))
		for _, model := range models {
			out = append(out, model.Name)
		}
		return out
	}

	It("keeps the whole chain when nothing is disabled", func() {
		Expect(names(chain.Candidates())).To(Equal([]string{"claude-sonnet-5", "gpt-5.6-sol", "claude-haiku-4-5"}))
	})

	It("drops the candidates whose provider is disabled", func() {
		install(NewDisabledSet(nil, []string{"openai"}, nil, nil, nil))

		Expect(names(chain.Candidates())).To(Equal([]string{"claude-sonnet-5", "claude-haiku-4-5"}))
	})

	It("drops a single model named on its own backend", func() {
		install(NewDisabledSet(nil, nil, nil, []string{"anthropic/claude-sonnet-5"}, nil))

		Expect(names(chain.Candidates())).To(Equal([]string{"gpt-5.6-sol", "claude-haiku-4-5"}))
	})

	It("substitutes an enabled model when the whole chain is disabled", func() {
		install(NewDisabledSet(nil, []string{"anthropic", "openai"}, nil, nil, nil))

		got := chain.Candidates()

		Expect(got).To(HaveLen(1))
		Expect(got[0].Backend.Provider()).NotTo(BeElementOf(Backend("anthropic"), Backend("openai")))
		Expect(got[0].Name).NotTo(BeEmpty())
	})

	It("returns the unfiltered chain when every backend is disabled, so the caller reports the dead end", func() {
		providers := make([]string, 0, len(AllBackends()))
		for _, backend := range AllBackends() {
			providers = append(providers, string(backend.Provider()))
		}
		install(NewDisabledSet(nil, providers, nil, nil, nil))

		Expect(names(chain.Candidates())).To(Equal([]string{"claude-sonnet-5", "gpt-5.6-sol", "claude-haiku-4-5"}))
	})

	It("keeps a name it cannot map to a backend rather than silently dropping it", func() {
		install(NewDisabledSet(nil, []string{"anthropic"}, nil, nil, nil))

		got := Model{Name: "claude-sonnet-5", Fallbacks: []Model{{Name: "not-a-real-model-family"}}}.Candidates()

		Expect(names(got)).To(Equal([]string{"not-a-real-model-family"}))
	})
})
