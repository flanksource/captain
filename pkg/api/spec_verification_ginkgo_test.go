package api_test

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("model-free verification spec validation", func() {
	It("accepts inherited runtime options without requiring an unused model", func() {
		spec := api.Spec{Model: api.Model{Mode: api.ModeAgent, Effort: api.EffortHigh}, Workflow: &api.Workflow{Verify: &api.Verify{Fixture: "acceptance"}}}
		Expect(spec.Validate()).To(Succeed())
	})
	It("still rejects malformed inherited tuning", func() {
		spec := api.Spec{Model: api.Model{Mode: "invalid"}, Workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}}
		Expect(spec.Validate()).To(MatchError(ContainSubstring("mode")))
	})
	It("requires a model for a judge prompt", func() {
		spec := api.Spec{Model: api.Model{Mode: api.ModeAgent}, Workflow: &api.Workflow{Verify: &api.Verify{Prompts: []string{"judge.prompt"}}}}
		Expect(spec.Validate()).To(MatchError(ContainSubstring("model name")))
	})
})
