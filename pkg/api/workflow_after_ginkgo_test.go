package api

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("Workflow.After", func() {
	It("decodes the frontmatter shape and defaults when to onVerify", func() {
		var wf Workflow
		Expect(yaml.Unmarshal([]byte(`
after:
  - cwd: /src/repo
    commands: ["git merge --ff-only topic"]
  - when: always
    commands: ["git worktree prune"]
`), &wf)).To(Succeed())
		Expect(wf.After).To(Equal([]AfterStep{
			{Cwd: "/src/repo", HookAction: HookAction{Commands: []string{"git merge --ff-only topic"}}},
			{When: CommitWhenAlways, HookAction: HookAction{Commands: []string{"git worktree prune"}}},
		}))
		Expect(wf.After[0].EffectiveWhen()).To(Equal(CommitWhenVerify))
		Expect(wf.After[1].EffectiveWhen()).To(Equal(CommitWhenAlways))
		Expect(wf.Validate()).To(Succeed())
	})

	DescribeTable("rejects a step that could never run as declared",
		func(step AfterStep, want string) {
			err := (&Workflow{After: []AfterStep{step}}).Validate()
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("unknown gate", AfterStep{When: "sometimes", HookAction: HookAction{Commands: []string{"true"}}}, "workflow.after[0]: when"),
		Entry("no action", AfterStep{}, "workflow.after[0]: declares no action"),
		Entry("blank command", AfterStep{HookAction: HookAction{Commands: []string{"true", "  "}}}, "workflow.after[0]: commands[1] is empty"),
		Entry("cwd on a tool", AfterStep{Cwd: "sub", HookAction: HookAction{Tool: "t"}}, "cwd is not read by a tool step"),
	)
})
