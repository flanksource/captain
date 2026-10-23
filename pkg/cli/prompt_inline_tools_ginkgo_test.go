package cli

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

// Inline tools are bound by promptrun alone, so every path that calls a provider
// directly must either route through the runner or refuse — a direct call would
// run the prompt with its declared tools silently missing.
var _ = Describe("Prompt runs that declare inline tools", func() {
	tools := api.InlineTools{"lookup": {Exec: "true", ReadOnly: true}}

	DescribeTable("need the runner-backed path",
		func(spec api.Spec, want bool) {
			Expect(requiresRunner(spec)).To(Equal(want))
		},
		Entry("a plain prompt runs directly", api.Spec{}, false),
		Entry("inline tools need the runner", api.Spec{Tools: tools}, true),
		Entry("a workflow needs the runner", api.Spec{Workflow: &api.Workflow{After: []api.AfterStep{{Commands: []string{"true"}}}}}, true),
	)

	It("refuses a chat launch, whose provider would never expose them", func() {
		rendered := PromptRenderResult{Input: api.Spec{Tools: tools}}

		_, err := launchRendered(context.Background(), "inline", rendered, true)

		Expect(err).To(MatchError("chat mode does not support prompts with inline tools"))
	})
})
