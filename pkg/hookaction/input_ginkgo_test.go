package hookaction_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/hookaction"
)

var _ = Describe("ApplyInputHooks", func() {
	const issueFile = "/issues/KEY-1.md"
	spec := func(hooks ...api.HookAction) *api.Spec {
		return &api.Spec{
			Model:  api.Model{Name: "jev-latest"},
			Labels: map[string]string{"team": "ops"},
			Prompt: api.Prompt{
				Variables: map[string]any{"file": issueFile}, Metadata: map[string]string{"source": "jira"},
				InputHooks: hooks,
			},
		}
	}

	It("replaces the vars with what each hook returns, the next hook seeing the previous one's", func() {
		loaded := map[string]any{"file": issueFile, "issue": "Annuity quote fails"}
		runner := &fakeRunner{answers: map[string]any{
			"load":        map[string]any{"vars": loaded},
			"tidy.prompt": `{"vars": {"issue": "annuity quote fails"}}`,
		}}
		s := spec(api.HookAction{Tool: "load", With: map[string]any{"path": "{{vars.file}}"}}, api.HookAction{Prompt: "tidy.prompt"})

		skip, err := hookaction.ApplyInputHooks(context.Background(), s, runner, nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(skip).To(BeNil())
		Expect(s.Prompt.Variables).To(Equal(map[string]any{"issue": "annuity quote fails"}))
		Expect(mustJSON(runner.calls[0].Input)).To(MatchJSON(`{"vars": {"file": "/issues/KEY-1.md"}, "metadata": {"source": "jira"}, "path": "/issues/KEY-1.md"}`))
		Expect(runner.calls[1].Input["vars"]).To(Equal(loaded))
	})

	It("stops at a hook that asks to skip, running none after it", func() {
		runner := &fakeRunner{answers: map[string]any{"load": map[string]any{"skip": "classified for issue update 2026-10-01"}}}
		s := spec(api.HookAction{Tool: "load"}, api.HookAction{Tool: "never"})

		skip, err := hookaction.ApplyInputHooks(context.Background(), s, runner, nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(skip).To(Equal(&hookaction.Skip{Hook: "input.hooks[0] (tool load)", Reason: "classified for issue update 2026-10-01"}))
		Expect(runner.calls).To(HaveLen(1))
		Expect(s.Prompt.Variables).To(Equal(map[string]any{"file": issueFile}))
	})

	It("skips a hook whose if is false, saying why", func() {
		runner := &fakeRunner{}
		var notices []string
		s := spec(api.HookAction{Tool: "load", If: `labels.team == "dev"`})

		skip, err := hookaction.ApplyInputHooks(context.Background(), s, runner, func(text string) { notices = append(notices, text) })

		Expect(err).NotTo(HaveOccurred())
		Expect(skip).To(BeNil())
		Expect(runner.calls).To(BeEmpty())
		Expect(notices).To(Equal([]string{`[pre-run] input.hooks[0] (tool load) skipped: if labels.team == "dev" is false`}))
	})

	DescribeTable("fails on output that is not exactly one of vars or skip, naming the hook",
		func(answer any, want string) {
			_, err := hookaction.ApplyInputHooks(context.Background(), spec(api.HookAction{Tool: "load"}), &fakeRunner{answers: map[string]any{"load": answer}}, nil)
			Expect(err).To(MatchError(ContainSubstring("input.hooks[0] (tool load): " + want)))
		},
		Entry("not JSON", "plain text", "output is not JSON"),
		Entry("not an object", []any{"a"}, "want a JSON object"),
		Entry("neither key", map[string]any{"issue": "x"}, `want an object with exactly one of "vars" or "skip", got keys`),
		Entry("both keys", map[string]any{"vars": map[string]any{}, "skip": "done"}, `want an object with exactly one of "vars" or "skip", got keys`),
		Entry("vars not an object", map[string]any{"vars": "x"}, `"vars" must be an object`),
		Entry("empty skip reason", map[string]any{"skip": ""}, `"skip" must be a non-empty reason`),
	)
})
