package api

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("HookAction", func() {
	It("decodes an after step's tool action inline beside its outcome gate", func() {
		var wf Workflow
		Expect(yaml.Unmarshal([]byte(`
after:
  - when: onSuccess
    if: output.value.product != "unknown"
    tool: set_frontmatter
    with: {file: "{{vars.file}}", count: 2}
`), &wf)).To(Succeed())
		Expect(wf.After).To(Equal([]AfterStep{{When: CommitWhenSuccess, HookAction: HookAction{
			If: `output.value.product != "unknown"`, Tool: "set_frontmatter",
			With: map[string]any{"file": "{{vars.file}}", "count": 2},
		}}}))
		Expect(wf.Validate()).To(Succeed())
	})

	DescribeTable("rejects an action that cannot run as declared",
		func(env HookEnv, action HookAction, want string) {
			Expect(action.Validate(env)).To(MatchError(ContainSubstring(want)))
		},
		Entry("no action", HookEnvLifecycle, HookAction{}, "declares no action"),
		Entry("two actions", HookEnvLifecycle, HookAction{Tool: "t", Prompt: "p.prompt"}, "declares 2 actions"),
		Entry("with on commands", HookEnvLifecycle, HookAction{Commands: []string{"true"}, With: map[string]any{"a": 1}}, "with is only read"),
		Entry("syntax error", HookEnvLifecycle, HookAction{Tool: "t", If: "output.value ==="}, `if "output.value ==="`),
		Entry("lifecycle-only variable in a schema hook", HookEnvSchema, HookAction{Tool: "t", If: "output.ok"}, "undeclared reference to 'output'"),
		Entry("non-bool predicate", HookEnvLifecycle, HookAction{Tool: "t", If: "result"}, "must return bool"),
	)

	It("evaluates if over the lifecycle variables", func() {
		action := HookAction{Tool: "t", If: `output.value.product != "unknown" && outcome.verified && size(steps) == 1`}
		vars := map[string]any{
			"output": map[string]any{"value": map[string]any{"product": "Annuity"}}, "result": "", "vars": map[string]any{},
			"metadata": map[string]string{}, "outcome": map[string]bool{"failed": false, "verified": true}, "steps": []any{nil},
		}
		Expect(action.EvalIf(HookEnvLifecycle, vars)).To(BeTrue())
		vars["output"] = map[string]any{"value": map[string]any{"product": "unknown"}}
		Expect(action.EvalIf(HookEnvLifecycle, vars)).To(BeFalse())
	})

	Context("against the spec's inline tools", func() {
		tools := InlineTools{
			"emit":  {Exec: "cat", ReadOnly: true},
			"write": {Exec: "cat", Destructive: true},
		}
		DescribeTable("refuses a hook the spec cannot serve",
			func(spec Spec, want string) {
				spec.Tools = tools
				Expect(spec.ValidateStructure()).To(MatchError(ContainSubstring(want)))
			},
			Entry("unknown after tool", Spec{Workflow: &Workflow{After: []AfterStep{{HookAction: HookAction{Tool: "missing"}}}}},
				`workflow.after[0].tool "missing" is not declared in tools`),
			Entry("unknown schema tool", Spec{Prompt: Prompt{SchemaHooks: []HookAction{{Tool: "missing"}}}},
				`output.hooks[0].tool "missing" is not declared in tools`),
			Entry("non-readonly schema tool", Spec{Prompt: Prompt{SchemaHooks: []HookAction{{Tool: "write"}}}},
				`output.hooks[0].tool "write" is not readonly`),
			Entry("commands schema hook", Spec{Prompt: Prompt{SchemaHooks: []HookAction{{Commands: []string{"true"}}}}},
				"output.hooks[0]: a schema hook runs a tool or a prompt"),
			Entry("non-readonly input tool", Spec{Prompt: Prompt{InputHooks: []HookAction{{Tool: "write"}}}},
				`input.hooks[0].tool "write" is not readonly`),
			Entry("commands input hook", Spec{Prompt: Prompt{InputHooks: []HookAction{{Commands: []string{"true"}}}}},
				"input.hooks[0]: an input hook runs a tool or a prompt"),
			Entry("lifecycle variable in an input hook", Spec{Prompt: Prompt{InputHooks: []HookAction{{Tool: "emit", If: "output.ok"}}}},
				"input.hooks[0]: if \"output.ok\""),
		)

		It("accepts a readonly schema tool and a destructive after tool", func() {
			spec := Spec{Tools: tools, Prompt: Prompt{SchemaHooks: []HookAction{{Tool: "emit"}}},
				Workflow: &Workflow{After: []AfterStep{{HookAction: HookAction{Tool: "write"}}}}}
			Expect(spec.ValidateStructure()).To(Succeed())
		})
	})
})
