package cli

import (
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

// Inline tools are bound by promptrun, so every path that calls a provider
// directly must route through the runner or bind them itself, as a chat does —
// a direct call would run the prompt with its declared tools silently missing.
var _ = Describe("Prompt runs that declare inline tools", func() {
	tools := api.InlineTools{"lookup": {Exec: "true", ReadOnly: true}}

	DescribeTable("need the runner-backed path",
		func(spec api.Spec, want bool) {
			Expect(requiresRunner(spec)).To(Equal(want))
		},
		Entry("a plain prompt runs directly", api.Spec{}, false),
		Entry("inline tools need the runner", api.Spec{Tools: tools}, true),
		Entry("a workflow needs the runner", api.Spec{Workflow: &api.Workflow{After: []api.AfterStep{{HookAction: api.HookAction{Commands: []string{"true"}}}}}}, true),
	)

	It("binds them for a chat, which refuses one nobody could approve before building a provider", func() {
		rendered := PromptRenderResult{
			Provider: api.Anthropic.Name, Mode: string(api.ModeAgent),
			Input:  api.Spec{Tools: api.InlineTools{"drop_table": {Exec: "true", Destructive: true}}},
			Config: api.Config{Model: api.Model{Name: "sonnet", Mode: api.ModeAgent}},
		}
		rendered.Input.SetCwd(GinkgoT().TempDir())
		chat := newChatSession("chat-tools", rendered, 0, newRunStream(), nil)

		handle := task.StartTask("chat run", func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
			return chat.run(t)
		})
		handle.WaitFor()
		_, err := handle.GetResult()

		Expect(err).To(MatchError(ContainSubstring("caller tools drop_table resolve to ask")))
	})
})
