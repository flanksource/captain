package provider

import (
	"os"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex app-server permission requests", func() {
	const method = "item/permissions/requestApproval"
	const rebase = `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-7","cwd":"/repo/worktree","startedAtMs":1,
		"reason":"Rebase must update Git metadata",
		"permissions":{"fileSystem":{"entries":[{"path":{"type":"path","path":"/repo/gavel/.git"},"access":"write"}]},"network":{"enabled":true}}}`
	noGrant := map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	gitWrite := map[string]any{"entries": []map[string]any{{"path": map[string]any{"type": "path", "path": "/repo/gavel/.git"}, "access": "write"}}}

	It("asks for the requested grant in the sandbox vocabulary", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)

		_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))

		Expect(rpcErr).To(BeNil())
		Expect(recorder.received()).To(Equal([]api.ApprovalRequest{{
			Tool: "request_permissions", Input: rawInput(rebase), ToolUseID: "item-7", SessionID: "thread-1",
			Kind: api.ApprovalKindPermissions, TurnID: "turn-1", Reason: "Rebase must update Git metadata",
			Escalates: true, SupportedScopes: []api.ApprovalScope{api.ApprovalScopeTurn, api.ApprovalScopeSession},
			Info: &api.ToolInfo{Name: "request_permissions"},
			Permissions: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/gavel/.git"}},
				Network:    &api.SandboxNetworkPolicy{Access: api.SandboxNetworkUnrestricted},
			},
		}}))
	})

	DescribeTable("translates the decision into a granted profile",
		func(decision api.ApprovalDecision, want map[string]any) {
			recorder := &approvalRecorder{decision: decision}
			c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
			answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))
			Expect(rpcErr).To(BeNil())
			Expect(answer).To(Equal(want))
		},
		Entry("allow without grants grants everything requested for the turn",
			api.ApprovalDecision{Allow: true},
			map[string]any{"permissions": map[string]any{"fileSystem": gitWrite, "network": map[string]any{"enabled": true}}, "scope": "turn"}),
		Entry("a filesystem subset for the turn withholds network",
			api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeTurn, Grants: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/gavel/.git"}},
			}},
			map[string]any{"permissions": map[string]any{"fileSystem": gitWrite}, "scope": "turn"}),
		Entry("network alone for the session",
			api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeSession, Grants: &api.NativeSandboxPolicy{
				Network: &api.SandboxNetworkPolicy{Access: api.SandboxNetworkUnrestricted},
			}},
			map[string]any{"permissions": map[string]any{"network": map[string]any{"enabled": true}}, "scope": "session"}),
		Entry("deny grants nothing", api.ApprovalDecision{Allow: false}, noGrant),
	)

	DescribeTable("refuses a grant the request or the run cannot take",
		func(run ai.Request, decision api.ApprovalDecision, wantErr string) {
			recorder := &approvalRecorder{decision: decision}
			c, _ := codexApprovalHarness(recorder.approve, run)
			_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))
			Expect(rpcErr).NotTo(BeNil())
			Expect(rpcErr.Message).To(ContainSubstring(wantErr))
		},
		Entry("a root Codex did not request", workspaceRun,
			api.ApprovalDecision{Allow: true, Grants: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/other"}},
			}}, `writable root "/repo/other" was not requested`),
		Entry("interrupt, which a permissions request cannot take", workspaceRun,
			api.ApprovalDecision{Interrupt: true}, "cannot interrupt"),
		Entry("a grant overlapping the run's denied roots",
			ai.Request{Setup: &shell.Setup{Cwd: "/repo/worktree"}, Sandbox: &api.SandboxRef{Mode: api.SandboxNative, Policy: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{DeniedWriteRoots: []string{"/repo/gavel"}},
			}}},
			api.ApprovalDecision{Allow: true}, `overlaps denied root "/repo/gavel"`),
	)

	It("resolves special, relative and legacy paths against the request cwd", func() {
		DeferCleanup(os.Setenv, "TMPDIR", os.Getenv("TMPDIR"))
		Expect(os.Setenv("TMPDIR", "/private/var/tmp-x")).To(Succeed())
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		run := workspaceRun
		run.Setup = &shell.Setup{Cwd: "/repo/worktree"}
		run.Permissions.Directories = []string{"/repo/shared"}
		c, _ := codexApprovalHarness(recorder.approve, run)
		params := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-7","cwd":"/repo/worktree","permissions":{"fileSystem":{
			"entries":[
				{"path":{"type":"special","value":{"kind":"tmpdir"}},"access":"write"},
				{"path":{"type":"special","value":{"kind":"slash_tmp"}},"access":"write"},
				{"path":{"type":"special","value":{"kind":"project_roots","subpath":".git"}},"access":"write"},
				{"path":{"type":"glob_pattern","pattern":"docs"},"access":"read"},
				{"path":{"type":"path","path":"../notes"},"access":"read"}],
			"read":["/etc/hosts"],"write":["build"]}}}`

		_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, params))

		Expect(rpcErr).To(BeNil())
		Expect(recorder.received()[0].Permissions).To(Equal(&api.NativeSandboxPolicy{Filesystem: &api.SandboxFilesystemPolicy{
			WritableRoots: []string{"/private/var/tmp-x", "/tmp", "/repo/worktree/.git", "/repo/shared/.git", "/repo/worktree/build"},
			ReadableRoots: []string{"/repo/worktree/docs", "/repo/notes", "/etc/hosts"},
		}}))
	})

	DescribeTable("refuses a requested path it cannot resolve to a concrete root",
		func(entry, wantErr string) {
			recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
			c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
			params := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-7","cwd":"/repo/worktree","permissions":{"fileSystem":{"entries":[` + entry + `]}}}`
			_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, params))
			Expect(rpcErr).NotTo(BeNil())
			Expect(rpcErr.Message).To(ContainSubstring(wantErr))
			Expect(recorder.received()).To(BeEmpty())
		},
		Entry("a glob", `{"path":{"type":"glob_pattern","pattern":"src/**/*.go"},"access":"write"}`, `glob "src/**/*.go"`),
		Entry("the minimal special path", `{"path":{"type":"special","value":{"kind":"minimal"}},"access":"read"}`, `special path "minimal"`),
		Entry("an unknown special path", `{"path":{"type":"special","value":{"kind":"unknown","path":"x"}},"access":"read"}`, `special path "unknown"`),
		Entry("deny access", `{"path":{"type":"path","path":"/repo/secret"},"access":"deny"}`, `access "deny"`),
	)

	It("grants nothing without a callback, when the tool policy denies it, and in plan mode", func() {
		c, _ := codexApprovalHarness(nil, workspaceRun)
		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))
		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(noGrant))

		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		denied := workspaceRun
		denied.ToolPreferences = api.ToolPreferences{"request_permissions": api.ToolPolicyDeny}
		c, _ = codexApprovalHarness(recorder.approve, denied)
		answer, rpcErr = c.handleApproval(codexServerRequest(`1`, method, rebase))
		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(noGrant))

		plan := workspaceRun
		plan.Permissions = api.Permissions{Mode: api.PermissionPlan}
		c, _ = codexApprovalHarness(recorder.approve, plan)
		answer, rpcErr = c.handleApproval(codexServerRequest(`1`, method, rebase))
		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(noGrant))
		Expect(recorder.received()).To(BeEmpty())
	})

	It("grants nothing when a legacy callback skips the request", func() {
		legacy := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(api.LegacyApprovalFunc(legacy.approve, "Config.CanUseTool", ""), workspaceRun)
		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))
		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(noGrant))
		Expect(legacy.received()).To(BeEmpty())
	})
})
