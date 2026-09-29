package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("ApprovalRequest.Validate", func() {
	DescribeTable("rejects a request whose payload does not match its kind",
		func(request api.ApprovalRequest, want string) {
			Expect(request.Validate()).To(MatchError(ContainSubstring(want)))
		},
		Entry("empty kind", api.ApprovalRequest{Tool: "Bash"}, "kind"),
		Entry("unknown kind", api.ApprovalRequest{Tool: "Bash", Kind: "shell"}, "kind"),
		Entry("missing tool", api.ApprovalRequest{Kind: api.ApprovalKindTool}, "tool"),
		Entry("command without its payload", api.ApprovalRequest{Tool: "Bash", Kind: api.ApprovalKindCommand}, "command"),
		Entry("tool kind carrying a command payload",
			api.ApprovalRequest{Tool: "Bash", Kind: api.ApprovalKindTool, Command: &api.CommandApproval{Command: "ls"}}, "command"),
		Entry("question without questions", api.ApprovalRequest{Tool: "AskUserQuestion", Kind: api.ApprovalKindQuestion}, "question"),
		Entry("plan without its plan", api.ApprovalRequest{Tool: "ExitPlanMode", Kind: api.ApprovalKindPlan}, "plan"),
		Entry("plan with empty content",
			api.ApprovalRequest{Tool: "ExitPlanMode", Kind: api.ApprovalKindPlan, Plan: &api.TerminalPlan{Content: "  "}}, "content"),
		Entry("tool kind carrying a plan",
			api.ApprovalRequest{Tool: "ExitPlanMode", Kind: api.ApprovalKindTool, Plan: &api.TerminalPlan{Content: "Ship it"}}, "plan"),
		Entry("unknown scope",
			api.ApprovalRequest{Tool: "Bash", Kind: api.ApprovalKindTool, SupportedScopes: []api.ApprovalScope{"forever"}}, "scope"),
	)

	It("accepts a tool request shaped exactly like the pre-rename PermissionRequest plus its kind", func() {
		Expect(api.ApprovalRequest{Tool: "Edit", Input: map[string]any{"file_path": "a.go"}, ToolUseID: "toolu_1", Kind: api.ApprovalKindTool}.Validate()).To(Succeed())
	})
})

var _ = Describe("ApprovalDecision.Validate", func() {
	command := api.ApprovalRequest{
		Tool: "exec_command", Kind: api.ApprovalKindCommand, ToolUseID: "appr_1",
		Command:         &api.CommandApproval{Command: "git rebase --continue"},
		SupportedScopes: []api.ApprovalScope{api.ApprovalScopeRequest, api.ApprovalScopeSession},
	}
	permissions := api.ApprovalRequest{
		Tool: "request_permissions", Kind: api.ApprovalKindPermissions, ToolUseID: "item_1",
		SupportedScopes: []api.ApprovalScope{api.ApprovalScopeTurn, api.ApprovalScopeSession},
		Permissions: &api.NativeSandboxPolicy{
			Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/.git"}},
			Network:    &api.SandboxNetworkPolicy{Access: api.SandboxNetworkUnrestricted},
		},
	}
	questions := api.ApprovalRequest{
		Tool: "AskUserQuestion", Kind: api.ApprovalKindQuestion, ToolUseID: "item_q",
		Questions: []api.TerminalQuestion{{ID: "data_model", Text: "Which model?", Options: []string{"Generic", "Replace"}}},
	}
	form := api.ApprovalRequest{
		Tool: "Elicitation", Kind: api.ApprovalKindElicitation, ToolUseID: "elicit:7", Interruptible: true,
		Elicitation: &api.ElicitationApproval{Server: "github", Mode: api.ElicitationModeForm, Message: "Which repository?",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo":   map[string]any{"type": "string", "enum": []any{"flanksource/captain", "flanksource/gavel"}},
					"labels": map[string]any{"type": "string"},
				},
				"required": []any{"repo"},
			}},
	}
	plan := api.ApprovalRequest{
		Tool: "ExitPlanMode", Kind: api.ApprovalKindPlan, ToolUseID: "toolu_plan", Interruptible: true,
		Input: map[string]any{"plan": "1. Add the kind"}, Plan: &api.TerminalPlan{Content: "1. Add the kind", Path: "/home/me/.claude/plans/p.md"},
	}
	url := api.ApprovalRequest{
		Tool: "Elicitation", Kind: api.ApprovalKindElicitation, ToolUseID: "elicit:8",
		Elicitation: &api.ElicitationApproval{Server: "linear", Mode: api.ElicitationModeURL, Message: "Authorize", URL: "https://linear.example/oauth"},
	}

	DescribeTable("accepts decisions the request supports",
		func(request api.ApprovalRequest, decision api.ApprovalDecision) {
			Expect(decision.Validate(request)).To(Succeed())
		},
		Entry("a pre-rename allow on a tool request",
			api.ApprovalRequest{Tool: "Edit", Kind: api.ApprovalKindTool}, api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"file_path": "b.go"}}),
		Entry("a pre-rename deny", command, api.ApprovalDecision{Message: "no"}),
		Entry("session scope the request offers", command, api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeSession}),
		Entry("a subset of the requested grants", permissions,
			api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeTurn, Grants: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/.git"}}}}),
		Entry("allow without grants, which grants everything requested", permissions, api.ApprovalDecision{Allow: true}),
		Entry("answers keyed by question id", questions,
			api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"answers": map[string]any{"data_model": "Generic"}}}),
		Entry("a plain approval of a question", questions, api.ApprovalDecision{Allow: true}),
		Entry("form content matching the schema", form,
			api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"repo": "flanksource/captain", "labels": "bug"}}),
		Entry("cancel on an interruptible request", form, api.ApprovalDecision{Interrupt: true}),
		Entry("url-mode completion without content", url, api.ApprovalDecision{Allow: true}),
		Entry("approving a plan", plan, api.ApprovalDecision{Allow: true}),
		Entry("sending a plan back with feedback", plan, api.ApprovalDecision{Message: "Split phase 2"}),
		Entry("cancelling at the plan", plan, api.ApprovalDecision{Interrupt: true}),
	)

	DescribeTable("rejects decisions the request cannot take",
		func(request api.ApprovalRequest, decision api.ApprovalDecision, want string) {
			Expect(decision.Validate(request)).To(MatchError(ContainSubstring(want)))
		},
		Entry("interrupt on a request that is not interruptible", command, api.ApprovalDecision{Interrupt: true}, "interrupt"),
		Entry("interrupt together with allow", form, api.ApprovalDecision{Allow: true, Interrupt: true}, "interrupt"),
		Entry("a scope the request does not offer", command, api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeTurn}, "scope"),
		Entry("a scoped deny", command, api.ApprovalDecision{Scope: api.ApprovalScopeSession}, "scope"),
		Entry("grants on a command", command, api.ApprovalDecision{Allow: true, Grants: &api.NativeSandboxPolicy{}}, "grants"),
		Entry("an unrequested writable root", permissions,
			api.ApprovalDecision{Allow: true, Grants: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/etc"}}}}, "/etc"),
		Entry("an unrequested domain", permissions,
			api.ApprovalDecision{Allow: true, Grants: &api.NativeSandboxPolicy{
				Network: &api.SandboxNetworkPolicy{AllowedDomains: []string{"example.com"}}}}, "example.com"),
		Entry("a grant field no request can carry", permissions,
			api.ApprovalDecision{Allow: true, Grants: &api.NativeSandboxPolicy{
				Commands: &api.SandboxCommandPolicy{ExcludedFromSandbox: []string{"git"}}}}, "commands"),
		Entry("updated input on a permissions request", permissions,
			api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"x": 1}}, "updatedInput"),
		Entry("an answer to an unknown question", questions,
			api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"answers": map[string]any{"data_model": "Generic", "rate_inputs": "Files"}}}, "rate_inputs"),
		Entry("form content missing a required field", form,
			api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"labels": "bug"}}, "repo"),
		Entry("form content outside the enum", form,
			api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"repo": "someone/else"}}, "repo"),
		Entry("form content with an undeclared field", form,
			api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"repo": "flanksource/captain", "extra": true}}, "extra"),
		Entry("accepting a form without content", form, api.ApprovalDecision{Allow: true}, "content"),
		Entry("content on a url-mode elicitation", url,
			api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"done": true}}, "url"),
		Entry("editing the plan through updatedInput", plan,
			api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"plan": "something else"}}, "updatedInput"),
	)
})

var _ = Describe("NativeSandboxPolicy.GrantConflict", func() {
	policy := api.NativeSandboxPolicy{
		Filesystem: &api.SandboxFilesystemPolicy{DeniedWriteRoots: []string{"/repo/.git"}, DeniedReadRoots: []string{"/home/me/.ssh"}},
		Network:    &api.SandboxNetworkPolicy{DeniedDomains: []string{"evil.example", "*.tracker.example"}},
	}

	DescribeTable("reports a grant that overlaps a deny",
		func(grant api.NativeSandboxPolicy, want string) {
			Expect(policy.GrantConflict(grant)).To(MatchError(ContainSubstring(want)))
		},
		Entry("the denied root itself", api.NativeSandboxPolicy{Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/.git"}}}, "/repo/.git"),
		Entry("a path under a denied root", api.NativeSandboxPolicy{Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/.git/hooks"}}}, "/repo/.git"),
		Entry("a parent that contains a denied root", api.NativeSandboxPolicy{Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo"}}}, "/repo/.git"),
		Entry("a read under a denied read root", api.NativeSandboxPolicy{Filesystem: &api.SandboxFilesystemPolicy{ReadableRoots: []string{"/home/me/.ssh/id_ed25519"}}}, ".ssh"),
		Entry("a denied domain", api.NativeSandboxPolicy{Network: &api.SandboxNetworkPolicy{AllowedDomains: []string{"evil.example"}}}, "evil.example"),
		Entry("a subdomain of a denied wildcard", api.NativeSandboxPolicy{Network: &api.SandboxNetworkPolicy{AllowedDomains: []string{"a.tracker.example"}}}, "tracker.example"),
	)

	It("accepts a grant that stays clear of every deny", func() {
		Expect(policy.GrantConflict(api.NativeSandboxPolicy{
			Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/src"}, ReadableRoots: []string{"/home/me/docs"}},
			Network:    &api.SandboxNetworkPolicy{AllowedDomains: []string{"api.github.com"}},
		})).To(Succeed())
	})

	It("treats a sibling with a shared name prefix as clear of the deny", func() {
		Expect(policy.GrantConflict(api.NativeSandboxPolicy{
			Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/.github"}},
		})).To(Succeed())
	})
})
