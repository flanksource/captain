package verify

import (
	"context"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/hookaction"
)

type actionCall struct {
	Kind, Name, Cwd string
	Input           map[string]any
}

// fakeActions records each dispatched action and answers from a table.
type fakeActions struct {
	calls   []actionCall
	answers map[string]any
	fail    error
}

func (f *fakeActions) RunTool(_ context.Context, name string, input map[string]any) (any, error) {
	f.calls = append(f.calls, actionCall{Kind: "tool", Name: name, Input: input})
	return f.answers[name], f.fail
}

func (f *fakeActions) RunPrompt(_ context.Context, path string, vars map[string]any, cwd string) (any, error) {
	f.calls = append(f.calls, actionCall{Kind: "prompt", Name: path, Input: vars, Cwd: cwd})
	return f.answers[path], f.fail
}

var _ = Describe("workflow.after actions", func() {
	var actions *fakeActions

	BeforeEach(func() { actions = &fakeActions{answers: map[string]any{}} })

	post := func(hc *agent.HookContext, steps ...api.AfterStep) error {
		hooks, err := AfterHooksFor(&api.Workflow{After: steps}, Options{Actions: actions})
		Expect(err).NotTo(HaveOccurred())
		var errs []error
		for _, hook := range hooks {
			errs = append(errs, hook.(agent.Post).Post(hc, agent.PhaseRun))
		}
		return errors.Join(errs...)
	}
	classified := func() *agent.HookContext {
		return &agent.HookContext{
			Context:  context.Background(),
			Request:  &ai.Request{Prompt: api.Prompt{Variables: map[string]any{"file": "/issues/KEY-1.md"}}},
			Response: &ai.Response{Text: "classified", StructuredData: map[string]any{"value": map[string]any{"product": "Annuity", "score": 3}}},
			Verified: true,
		}
	}
	step := func(action api.HookAction) api.AfterStep {
		return api.AfterStep{When: api.CommitWhenSuccess, HookAction: action}
	}

	It("hands a tool step its with, binding a lone {{path}} to the native value", func() {
		Expect(post(classified(), step(api.HookAction{Tool: "set_frontmatter", With: map[string]any{
			"file": "{{vars.file}}", "values": "{{output.value}}", "label": "{{output.value.product}} via {{result}}",
		}}))).To(Succeed())

		Expect(actions.calls).To(Equal([]actionCall{{Kind: "tool", Name: "set_frontmatter", Input: map[string]any{
			"file": "/issues/KEY-1.md", "values": map[string]any{"product": "Annuity", "score": float64(3)}, "label": "Annuity via classified",
		}}}))
	})

	It("runs a step whose if holds and skips one whose if does not, saying why", func() {
		hc := classified()
		Expect(post(hc,
			step(api.HookAction{If: `output.value.product == "unknown"`, Tool: "never"}),
			step(api.HookAction{If: `output.value.product != "unknown" && outcome.verified`, Tool: "chosen"}),
		)).To(Succeed())

		Expect(actions.calls).To(HaveLen(1))
		Expect(actions.calls[0].Name).To(Equal("chosen"))
		Expect(hc.Workspace().Notices[0].Text).To(Equal(`[post-run] after[0] skipped: if output.value.product == "unknown" is false`))
	})

	It("feeds earlier steps' outputs forward to a child prompt alongside the run's output", func() {
		actions.answers["stage1"] = map[string]any{"written": true}
		hc := classified()
		hc.Workspace().Cwd = "/work"
		Expect(post(hc,
			step(api.HookAction{Tool: "stage1"}),
			step(api.HookAction{If: "steps[0].written", Prompt: "/prompts/transaction.prompt", With: map[string]any{
				"product": "{{output.value.product}}", "previous": "{{steps.0}}",
			}}),
		)).To(Succeed())

		Expect(actions.calls[1]).To(Equal(actionCall{Kind: "prompt", Name: "/prompts/transaction.prompt", Cwd: "/work", Input: map[string]any{
			"product": "Annuity", "previous": map[string]any{"written": true},
		}}))
	})

	It("fails the run when an action fails, naming the step", func() {
		actions.fail = errors.New("child prompt failed")
		err := post(classified(), step(api.HookAction{Prompt: "/prompts/transaction.prompt"}))
		Expect(err).To(MatchError(ContainSubstring(`workflow.after[0] prompt "/prompts/transaction.prompt": child prompt failed`)))
	})

	It("reports a child prompt its input hooks skip as a skipped step, not a failure", func() {
		actions.fail = fmt.Errorf("child: %w", &hookaction.Skip{Hook: "input.hooks[0] (tool load)", Reason: "already classified"})
		hc := classified()

		Expect(post(hc, step(api.HookAction{Prompt: "/prompts/transaction.prompt"}))).To(Succeed())

		Expect(hc.Workspace().Notices[0].Text).To(Equal("[post-run] after[0] skipped: already classified"))
	})

	It("refuses a tool or prompt step when the run supplies no action runner", func() {
		_, err := AfterHooksFor(&api.Workflow{After: []api.AfterStep{step(api.HookAction{Tool: "t"})}}, Options{})
		Expect(err).To(MatchError(ContainSubstring("workflow.after[0] runs a tool, but the run supplies no hook action runner")))
	})
})
