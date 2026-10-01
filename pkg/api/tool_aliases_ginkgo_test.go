package api_test

import (
	"maps"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

// claudeShell is what the shell alias expands to on claude, plus any other
// expected rules.
func claudeShell(policy api.ToolPolicy, extra api.Tools) api.Tools {
	out := api.Tools{"Bash": policy, "BashOutput": policy, "KillShell": policy, "Monitor": policy}
	maps.Copy(out, extra)
	return out
}

var _ = Describe("Tools.ForRuntime", func() {
	deny, allow, auto := api.ToolPolicyDeny, api.ToolPolicyAllow, api.ToolPolicyAuto

	DescribeTable("translates authored keys into the runtime's own tool names",
		func(provider *api.ModelProvider, mode api.RuntimeMode, authored, want api.Tools) {
			got, warnings := authored.ForRuntime(provider, mode)
			Expect(warnings).To(BeEmpty())
			Expect(got).To(Equal(want))
		},
		// --- shared aliases, per agent
		Entry("claude shell covers the background shell tools", api.Anthropic, api.ModeCLI, api.Tools{"shell": deny},
			api.Tools{"Bash": deny, "BashOutput": deny, "KillShell": deny, "Monitor": deny}),
		Entry("claude built-ins outside the alias table keep their policies", api.Anthropic, api.ModeCLI,
			api.Tools{"AskUserQuestion": deny, "ExitPlanMode": deny, "Agent": deny, "ToolSearch": deny, "Skill": allow},
			api.Tools{"AskUserQuestion": deny, "ExitPlanMode": deny, "Agent": deny, "ToolSearch": deny, "Skill": allow}),
		Entry("claude edit covers every editing tool", api.Anthropic, api.ModeAgent,
			api.Tools{"edit": deny}, api.Tools{"Edit": deny, "MultiEdit": deny, "NotebookEdit": deny}),
		Entry("claude write", api.Anthropic, api.ModeCmux, api.Tools{"write": deny}, api.Tools{"Write": deny, "NotebookEdit": deny}),
		Entry("claude search", api.Anthropic, api.ModeCLI, api.Tools{"search": allow}, api.Tools{"Glob": allow, "Grep": allow}),
		Entry("claude read, web and todo", api.Anthropic, api.ModeCLI,
			api.Tools{"read": auto, "web_fetch": deny, "web_search": deny, "todo": deny},
			api.Tools{"Read": auto, "WebFetch": deny, "WebSearch": deny,
				"TodoWrite": deny, "TaskCreate": deny, "TaskGet": deny, "TaskList": deny, "TaskUpdate": deny}),
		Entry("codex shell covers exec, exec_command and write_stdin", api.OpenAI, api.ModeCLI,
			api.Tools{"shell": deny}, api.Tools{"shell": deny, "exec": deny, "exec_command": deny, "write_stdin": deny}),
		Entry("codex edit and write are apply_patch", api.OpenAI, api.ModeAgent,
			api.Tools{"edit": deny, "write": deny}, api.Tools{"apply_patch": deny}),
		Entry("codex todo and web_search", api.OpenAI, api.ModeCmux,
			api.Tools{"todo": deny, "web_search": deny}, api.Tools{"update_plan": deny, "web_search": deny}),
		Entry("gemini shell, read and search", api.Google, api.ModeCLI,
			api.Tools{"shell": deny, "read": deny, "search": deny},
			api.Tools{"run_shell_command": deny, "read_file": deny, "read_many_files": deny,
				"glob": deny, "search_file_content": deny, "list_directory": deny}),
		Entry("gemini edit, write and web", api.Google, api.ModeCLI,
			api.Tools{"edit": deny, "write": deny, "web_fetch": deny, "web_search": deny},
			api.Tools{"replace": deny, "write_file": deny, "web_fetch": deny, "google_web_search": deny}),
		Entry("API keeps caller tool names", api.OpenAI, api.ModeAPI,
			api.Tools{"lookup_invoice": deny}, api.Tools{"lookup_invoice": deny}),

		// --- provider prefixes
		Entry("same-provider prefix by agent name", api.Anthropic, api.ModeCLI,
			api.Tools{"claude:Bash(git push:*)": deny}, api.Tools{"Bash(git push:*)": deny}),
		Entry("same-provider prefix by provider name", api.Google, api.ModeCLI,
			api.Tools{"google:run_shell_command": deny, "googleai:write_file": allow},
			api.Tools{"run_shell_command": deny, "write_file": allow}),
		Entry("other-provider prefix drops the key", api.Anthropic, api.ModeCLI,
			api.Tools{"codex:shell": deny, "gemini:read_file": allow}, api.Tools{}),
		Entry("a colon inside a pattern is not a prefix", api.Anthropic, api.ModeCLI,
			api.Tools{"Bash(git log:*)": allow, "shell": deny}, claudeShell(deny, api.Tools{"Bash(git log:*)": allow})),

		// --- passthrough and case
		Entry("mcp tools pass through", api.Anthropic, api.ModeAgent,
			api.Tools{"mcp__github__create_issue": deny}, api.Tools{"mcp__github__create_issue": deny}),
		Entry("tool names match case-insensitively", api.Anthropic, api.ModeCLI,
			api.Tools{"bash": deny, "READ": allow}, api.Tools{"Bash": deny, "Read": allow}),
		Entry("aliases match case-insensitively", api.Google, api.ModeCLI,
			api.Tools{"SHELL": deny}, api.Tools{"run_shell_command": deny}),
		Entry("an exact claude name stays precise", api.Anthropic, api.ModeCLI,
			api.Tools{"Edit": allow}, api.Tools{"Edit": allow}),

		// --- collisions only tighten
		Entry("a Bash allow never lifts a shell deny", api.Anthropic, api.ModeCLI,
			api.Tools{"Bash": allow, "shell": deny}, claudeShell(deny, nil)),
		Entry("an Edit auto never lifts an edit deny", api.Anthropic, api.ModeCLI,
			api.Tools{"Edit": auto, "edit": deny}, api.Tools{"Edit": deny, "MultiEdit": deny, "NotebookEdit": deny}),
		Entry("an alias allow reaches claude's tool", api.Anthropic, api.ModeCLI,
			api.Tools{"shell": allow}, claudeShell(allow, nil)),
		Entry("a claude name that another agent spells in lower case is foreign there", api.Google, api.ModeCLI,
			api.Tools{"Glob": deny}, api.Tools{"glob": deny, "search_file_content": deny, "list_directory": deny}),

		// --- other agents' built-ins
		Entry("a codex built-in on claude expands through its aliases", api.Anthropic, api.ModeCLI,
			api.Tools{"exec_command": deny, "apply_patch": allow},
			claudeShell(deny, api.Tools{"Edit": allow, "MultiEdit": allow, "NotebookEdit": allow, "Write": allow})),
		Entry("a patterned foreign deny fails closed to the bare tool", api.Google, api.ModeCLI,
			api.Tools{"Bash(git log:*)": deny}, api.Tools{"run_shell_command": deny}),
		Entry("a patterned foreign allow grants nothing", api.Anthropic, api.ModeCLI,
			api.Tools{"run_shell_command(git log:*)": allow}, api.Tools{}),
	)

	It("gives codex shell no read alias", func() {
		got, warnings := api.Tools{"read": api.ToolPolicyDeny}.ForRuntime(api.OpenAI, api.ModeCLI)
		Expect(got).To(Equal(api.Tools{}))
		Expect(warnings).To(ConsistOf(ContainSubstring(`"read"`)))
	})

	DescribeTable("warns for every ignored deny, and only for denies",
		func(provider *api.ModelProvider, mode api.RuntimeMode, authored api.Tools, ignored []string) {
			got, warnings := authored.ForRuntime(provider, mode)
			Expect(got).To(Equal(api.Tools{}))
			matchers := make([]any, len(ignored))
			for i, key := range ignored {
				matchers[i] = And(ContainSubstring(`"`+key+`"`), ContainSubstring("ignored"), ContainSubstring(api.RuntimeOf(provider, mode).String()))
			}
			Expect(warnings).To(ConsistOf(matchers...))
		},
		Entry("unknown names", api.Anthropic, api.ModeCLI,
			api.Tools{"NotATool": api.ToolPolicyDeny, "AlsoNotATool": api.ToolPolicyAllow}, []string{"NotATool"}),
		Entry("aliases and built-ins on the API mode", api.OpenAI, api.ModeAPI,
			api.Tools{"shell": api.ToolPolicyDeny, "Bash": api.ToolPolicyDeny, "Read": api.ToolPolicyAllow}, []string{"Bash", "shell"}),
		Entry("a foreign built-in this runtime has no counterpart for", api.Google, api.ModeCLI,
			api.Tools{"TodoWrite": api.ToolPolicyDeny, "spawn_agent": api.ToolPolicyDeny}, []string{"TodoWrite", "spawn_agent"}),
	)

	Describe("on runtimes without a per-tool filter", func() {
		claudeAllowlist := api.Tools{"Bash": allow, "Edit": allow, "Glob": allow, "Grep": allow, "Read": allow, "Write": allow, "shell": auto}

		DescribeTable("drops allows that arrive only through an alias or a foreign built-in",
			func(provider *api.ModelProvider, mode api.RuntimeMode) {
				got, warnings := claudeAllowlist.ForRuntime(provider, mode)
				Expect(warnings).To(BeEmpty())
				Expect(got.AllowList()).To(BeEmpty())
			},
			Entry("codex cli", api.OpenAI, api.ModeCLI),
			Entry("codex agent", api.OpenAI, api.ModeAgent),
			Entry("gemini cli", api.Google, api.ModeCLI),
		)

		It("keeps exact and prefixed allows of names that are not aliases so they are still refused", func() {
			got, _ := api.Tools{"exec_command": allow, "gemini:read_file": allow, "Bash": allow}.ForRuntime(api.OpenAI, api.ModeAgent)
			Expect(got).To(Equal(api.Tools{"exec_command": allow}))
			got, _ = api.Tools{"openai:apply_patch": allow, "APPLY_PATCH": allow}.ForRuntime(api.OpenAI, api.ModeCLI)
			Expect(got).To(Equal(api.Tools{"apply_patch": allow}))
			got, _ = api.Tools{"gemini:read_file": allow, "shell": allow}.ForRuntime(api.Google, api.ModeCLI)
			Expect(got).To(Equal(api.Tools{"read_file": allow}))
		})

		DescribeTable("drops an alias allow in every spelling, even where the alias is also a tool name",
			func(provider *api.ModelProvider, mode api.RuntimeMode, key string) {
				got, warnings := api.Tools{key: allow}.ForRuntime(provider, mode)
				Expect(warnings).To(BeEmpty())
				Expect(got).To(Equal(api.Tools{}))
				Expect(api.RequireToolPolicySupport(provider, mode, api.Permissions{Tools: api.Tools{key: allow}})).To(Succeed())
			},
			Entry("codex shell", api.OpenAI, api.ModeAgent, "shell"),
			Entry("codex Shell", api.OpenAI, api.ModeAgent, "Shell"),
			Entry("codex SHELL", api.OpenAI, api.ModeCLI, "SHELL"),
			Entry("codex web_search", api.OpenAI, api.ModeCLI, "web_search"),
			Entry("gemini web_fetch", api.Google, api.ModeCLI, "web_fetch"),
			Entry("a same-provider prefix still names the alias", api.OpenAI, api.ModeAgent, "openai:shell"),
			Entry("a patterned alias", api.OpenAI, api.ModeAgent, "shell(git status)"),
		)

		DescribeTable("keeps an alias deny in every spelling, and refuses it",
			func(key string) {
				got, _ := api.Tools{key: deny}.ForRuntime(api.OpenAI, api.ModeAgent)
				Expect(got).To(Equal(api.Tools{"shell": deny, "exec": deny, "exec_command": deny, "write_stdin": deny}))
				err := api.RequireToolPolicySupport(api.OpenAI, api.ModeAgent, api.Permissions{Tools: api.Tools{key: deny}})
				Expect(err).To(MatchError(ContainSubstring("openai agent cannot enforce a per-tool policy")))
			},
			Entry("shell", "shell"),
			Entry("Shell", "Shell"),
			Entry("SHELL", "SHELL"),
			Entry("openai:shell", "openai:shell"),
		)

		It("never drops a deny that arrives through a foreign built-in", func() {
			got, _ := api.Tools{"Bash": deny}.ForRuntime(api.OpenAI, api.ModeAgent)
			Expect(got).To(Equal(api.Tools{"shell": deny, "exec": deny, "exec_command": deny, "write_stdin": deny}))
		})
	})

	It("returns a translated copy with its ignored-deny warnings and keeps the authored permissions untouched", func() {
		authored := api.Permissions{Mode: api.PermissionPlan, Tools: api.Tools{"shell": deny, "NotATool": deny}}
		translated, warnings := authored.ForRuntime(api.Anthropic, api.ModeCLI)
		Expect(translated).To(Equal(api.Permissions{Mode: api.PermissionPlan, Tools: claudeShell(deny, nil)}))
		Expect(warnings).To(Equal([]string{`permissions.tools "NotATool" deny is ignored: anthropic cli has no tool it names`}))
		Expect(authored.Tools).To(Equal(api.Tools{"shell": deny, "NotATool": deny}))
	})
})

var _ = Describe("RequireToolPolicySupport with translated tools", func() {
	It("names the translated tool and the key it came from", func() {
		err := api.RequireToolPolicySupport(api.Google, api.ModeCLI, api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyDeny}})
		Expect(err).To(MatchError(ContainSubstring("google cli cannot enforce a per-tool policy (run_shell_command (from Bash))")))
	})

	It("refuses ask on the authored key before translation", func() {
		err := api.RequireToolPolicySupport(api.Anthropic, api.ModeCLI, api.Permissions{Tools: api.Tools{"shell": api.ToolPolicyAsk}})
		Expect(err).To(MatchError(ContainSubstring(`per-tool policy "ask" (shell)`)))
	})

	It("accepts an alias deny on a claude runtime", func() {
		Expect(api.RequireToolPolicySupport(api.Anthropic, api.ModeAgent, api.Permissions{Tools: api.Tools{"shell": api.ToolPolicyDeny}})).To(Succeed())
	})
})
