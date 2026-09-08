package query

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/database"
)

var _ = Describe("PromptRunFacts", func() {
	// The runtime carries what was asked for and what the registry actually
	// chose. Composition must not see both, so flattening them — resolved first,
	// requested as the fallback — is decided here, once.
	runtime := func(resolved, requested database.PromptRunRuntimeSelection) database.PromptRunRuntime {
		return database.PromptRunRuntime{Resolved: resolved, Requested: requested}
	}

	It("prefers what the runtime resolved over what was requested", func() {
		facts := PromptRunFacts(database.PromptRun{
			Runtime: runtime(
				database.PromptRunRuntimeSelection{Provider: "anthropic", Model: "claude-opus-5", Mode: "agent", Effort: "xhigh"},
				database.PromptRunRuntimeSelection{Provider: "openai", Model: "gpt-5.6-luna", Mode: "api", Effort: "low"},
			),
		})

		Expect(facts.Provider).To(Equal("anthropic"))
		Expect(facts.Model).To(Equal("claude-opus-5"))
		Expect(facts.Mode).To(Equal("agent"))
		Expect(facts.Effort).To(Equal("xhigh"))
	})

	It("falls back to the request when nothing was resolved", func() {
		// A run that never reached the registry — which is exactly the state a
		// run blocked on its first approval is in.
		facts := PromptRunFacts(database.PromptRun{
			Runtime: runtime(
				database.PromptRunRuntimeSelection{},
				database.PromptRunRuntimeSelection{Provider: "openai", Model: "gpt-5.6-luna", Mode: "api", Effort: "low"},
			),
		})

		Expect(facts.Provider).To(Equal("openai"))
		Expect(facts.Model).To(Equal("gpt-5.6-luna"))
		Expect(facts.Mode).To(Equal("api"))
		Expect(facts.Effort).To(Equal("low"))
	})

	It("mixes per field rather than choosing one selection wholesale", func() {
		facts := PromptRunFacts(database.PromptRun{
			Runtime: runtime(
				database.PromptRunRuntimeSelection{Model: "claude-opus-5"},
				database.PromptRunRuntimeSelection{Provider: "openai", Model: "gpt-5.6-luna"},
			),
		})

		Expect(facts.Model).To(Equal("claude-opus-5"))
		Expect(facts.Provider).To(Equal("openai"))
	})

	It("carries the run's failure, which the enricher branch used to drop", func() {
		facts := PromptRunFacts(database.PromptRun{
			Error: "provider refused: caller tools require MCP but MCP is disabled",
			State: database.PromptRunStateFailed,
		})

		Expect(facts.Error).To(ContainSubstring("caller tools require MCP"))
		Expect(facts.State).To(Equal(string(database.PromptRunStateFailed)))
	})
})
