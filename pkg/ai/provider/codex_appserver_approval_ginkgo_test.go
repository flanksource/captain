package provider

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// approvalRecorder is an OnApproval callback that records every request and
// answers each with the same decision.
type approvalRecorder struct {
	mu       sync.Mutex
	requests []api.ApprovalRequest
	decision api.ApprovalDecision
	err      error
}

func (r *approvalRecorder) approve(_ context.Context, request api.ApprovalRequest) (api.ApprovalDecision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	return r.decision, r.err
}

func (r *approvalRecorder) received() []api.ApprovalRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]api.ApprovalRequest(nil), r.requests...)
}

// codexApprovalHarness is a provider with an active turn on thread-1/turn-1,
// judging approvals against run.
func codexApprovalHarness(onApproval api.ApprovalFunc, run ai.Request) (*CodexAppServer, *turnState) {
	c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}, OnApproval: onApproval})
	Expect(err).NotTo(HaveOccurred())
	c.setPosture(postureFor(run))
	turn := &turnState{
		ctx: context.Background(), ch: make(chan ai.Event, 16), usage: &ai.Usage{},
		streamed: map[string]string{}, toolOutput: map[string]string{},
		terminal: make(chan struct{}), started: make(chan struct{}),
	}
	turn.setIDs("thread-1", "turn-1")
	c.setActive(turn)
	return c, turn
}

func codexServerRequest(id, method, params string) jsonrpc.ServerRequest {
	return jsonrpc.ServerRequest{ID: json.RawMessage(id), Method: method, Params: json.RawMessage(params)}
}

func rawInput(params string) map[string]any {
	var input map[string]any
	Expect(json.Unmarshal([]byte(params), &input)).To(Succeed())
	return input
}

var workspaceRun = ai.Request{
	Setup:       &shell.Setup{Cwd: "/repo/gavel"},
	Permissions: api.Permissions{Mode: api.PermissionDefault},
	Sandbox: &api.SandboxRef{Mode: api.SandboxNative, Policy: &api.NativeSandboxPolicy{
		Filesystem: &api.SandboxFilesystemPolicy{Access: api.SandboxFilesystemWorkspaceWrite},
	}},
}

var _ = Describe("Codex app-server command approvals", func() {
	const method = "item/commandExecution/requestApproval"
	const rebase = `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","approvalId":"appr-1","kind":"command",
		"command":"GIT_EDITOR=true git rebase --continue","cwd":"/repo/gavel","reason":"May I continue the active rebase?",
		"proposedExecpolicyAmendment":["git","rebase"],"startedAtMs":1}`

	It("routes a command approval to OnApproval keyed by its approvalId", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)

		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]string{"decision": "accept"}))
		Expect(recorder.received()).To(Equal([]api.ApprovalRequest{{
			Tool: "exec_command", Input: rawInput(rebase), ToolUseID: "appr-1", SessionID: "thread-1",
			Kind: api.ApprovalKindCommand, TurnID: "turn-1", Reason: "May I continue the active rebase?",
			Escalates: true, Interruptible: true,
			SupportedScopes: []api.ApprovalScope{api.ApprovalScopeRequest, api.ApprovalScopeSession},
			Info:            &api.ToolInfo{Name: "exec_command"},
			Command: &api.CommandApproval{
				Command: "GIT_EDITOR=true git rebase --continue", Cwd: "/repo/gavel", ProposedPolicy: []string{"git", "rebase"},
			},
		}}))
	})

	DescribeTable("translates the decision into Codex's command vocabulary",
		func(decision api.ApprovalDecision, want string) {
			recorder := &approvalRecorder{decision: decision}
			c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
			answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))
			Expect(rpcErr).To(BeNil())
			Expect(answer).To(Equal(map[string]string{"decision": want}))
		},
		Entry("approve", api.ApprovalDecision{Allow: true}, "accept"),
		Entry("approve for the session", api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeSession}, "acceptForSession"),
		Entry("deny", api.ApprovalDecision{Allow: false}, "decline"),
		Entry("cancel", api.ApprovalDecision{Allow: false, Interrupt: true}, "cancel"),
	)

	DescribeTable("refuses a decision Codex cannot take",
		func(params string, decision api.ApprovalDecision, wantErr string) {
			recorder := &approvalRecorder{decision: decision}
			c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
			_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, params))
			Expect(rpcErr).NotTo(BeNil())
			Expect(rpcErr.Message).To(ContainSubstring(wantErr))
		},
		Entry("updated input on a command", rebase, api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"command": "ls"}}, "updatedInput"),
		Entry("a turn scope", rebase, api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeTurn}, `scope "turn"`),
		Entry("cancel when availableDecisions omits it",
			`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"ls","availableDecisions":["accept","decline"]}`,
			api.ApprovalDecision{Interrupt: true}, "cannot interrupt"),
		Entry("a session approval when availableDecisions omits it",
			`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"ls","availableDecisions":["accept","decline"]}`,
			api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeSession}, `scope "session"`),
		Entry("an accept availableDecisions does not list",
			`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"ls","availableDecisions":["decline","cancel"]}`,
			api.ApprovalDecision{Allow: true}, `"accept" is not among`),
	)

	It("derives interruptible and scopes from availableDecisions, ignoring amendment offers", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: false}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		params := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"ls",
			"availableDecisions":["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["ls"]}},"decline"]}`

		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, params))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]string{"decision": "decline"}))
		Expect(recorder.received()[0].Interruptible).To(BeFalse())
		Expect(recorder.received()[0].SupportedScopes).To(Equal([]api.ApprovalScope{api.ApprovalScopeRequest}))
	})

	It("keys a regular approval, which Codex sends without an approvalId, on its itemId", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-9","approvalId":null,"command":"ls"}`))
		Expect(rpcErr).To(BeNil())
		Expect(recorder.received()[0].ToolUseID).To(Equal("item-9"))
	})

	It("asks for network access with the command that needs it", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeSession}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		params := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","approvalId":"appr-2","kind":"command",
			"command":"./.bin/gavel serve","networkApprovalContext":{"host":"api.github.com","protocol":"https"},
			"proposedNetworkPolicyAmendments":[{"host":"api.github.com","action":"allow"}]}`

		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, params))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]string{"decision": "acceptForSession"}))
		request := recorder.received()[0]
		Expect(request.Kind).To(Equal(api.ApprovalKindNetwork))
		Expect(request.Tool).To(Equal("exec_command"))
		Expect(request.Escalates).To(BeTrue())
		Expect(request.Network).To(Equal(&api.NetworkApproval{Host: "api.github.com", Protocol: "https", Command: "./.bin/gavel serve"}))
		Expect(request.Command).To(BeNil())
	})

	It("asks for input to a running terminal as a write_stdin command", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		params := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-cmd","approvalId":"appr-3","kind":"writeStdin","command":"n"}`

		_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, params))

		Expect(rpcErr).To(BeNil())
		request := recorder.received()[0]
		Expect(request.Tool).To(Equal("write_stdin"))
		Expect(request.ToolUseID).To(Equal("appr-3"))
		Expect(request.Command).To(Equal(&api.CommandApproval{Command: "n", Stdin: &api.StdinWrite{Terminal: "item-cmd"}}))
	})

	DescribeTable("refuses a command request it cannot key or classify",
		func(params, wantErr string) {
			recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
			c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
			_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, params))
			Expect(rpcErr).NotTo(BeNil())
			Expect(rpcErr.Message).To(ContainSubstring(wantErr))
			Expect(recorder.received()).To(BeEmpty())
		},
		Entry("writeStdin without an approvalId", `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","kind":"writeStdin"}`, "approvalId"),
		Entry("no itemId", `{"threadId":"thread-1","turnId":"turn-1","command":"ls"}`, "itemId"),
		Entry("an unknown kind", `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","kind":"resize"}`, `kind "resize"`),
		Entry("another thread", `{"threadId":"thread-2","turnId":"turn-1","itemId":"item-1","command":"ls"}`, "active thread"),
	)

	It("answers a legacy callback's skipped command from the posture without calling it", func() {
		legacy := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(api.LegacyApprovalFunc(legacy.approve, "Config.CanUseTool", ""), workspaceRun)

		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]string{"decision": "decline"}))
		Expect(legacy.received()).To(BeEmpty())
	})

	It("declines a command the run's tool policy denies without asking", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		run := workspaceRun
		run.Sandbox = &api.SandboxRef{Mode: api.SandboxOff}
		run.Permissions = api.Permissions{Mode: api.PermissionBypass}
		run.ToolPreferences = api.ToolPreferences{"exec_command": api.ToolPolicyDeny}
		c, _ := codexApprovalHarness(recorder.approve, run)

		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))
		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]string{"decision": "decline"}))
		Expect(recorder.received()).To(BeEmpty())

		c.cfg.OnApproval = nil
		answer, rpcErr = c.handleApproval(codexServerRequest(`1`, method, rebase))
		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]string{"decision": "decline"}), "a bypass posture does not lift the deny floor")
	})

	It("declines in plan mode without asking", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		run := workspaceRun
		run.Permissions = api.Permissions{Mode: api.PermissionPlan}
		c, _ := codexApprovalHarness(recorder.approve, run)

		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]string{"decision": "decline"}))
		Expect(recorder.received()).To(BeEmpty())
	})

	It("fails loudly when a callback is attached but no turn is active", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		c.setActive(nil)
		_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, rebase))
		Expect(rpcErr).NotTo(BeNil())
		Expect(rpcErr.Message).To(ContainSubstring("no active turn"))
	})

	DescribeTable("refuses the pre-v2 approval methods by name",
		func(legacyMethod string) {
			c, _ := codexApprovalHarness((&approvalRecorder{}).approve, workspaceRun)
			_, rpcErr := c.handleApproval(codexServerRequest(`1`, legacyMethod, `{}`))
			Expect(rpcErr).NotTo(BeNil())
			Expect(rpcErr.Message).To(ContainSubstring(legacyMethod))
		},
		Entry("execCommandApproval", "execCommandApproval"),
		Entry("applyPatchApproval", "applyPatchApproval"),
	)

	It("answers a server request it does not handle with method-not-found", func() {
		c, _ := codexApprovalHarness(nil, workspaceRun)
		_, rpcErr := c.handleApproval(codexServerRequest(`1`, "currentTime/read", `{}`))
		Expect(rpcErr).To(Equal(&jsonrpc.RPCError{Code: -32601, Message: "codex app-server request currentTime/read is not supported by captain"}))
	})
})

var _ = Describe("Codex app-server file-change approvals", func() {
	const method = "item/fileChange/requestApproval"
	started := func(path, kind string) json.RawMessage {
		return json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","startedAtMs":1,"item":{"type":"fileChange","id":"item-5","status":"inProgress",
			"changes":[{"path":"` + path + `","kind":{"type":"` + kind + `"},"diff":"-old\n+new"}]}}`)
	}
	approval := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-5","reason":null,"grantRoot":null,"startedAtMs":2}`

	It("joins the approval to its started item and flags a write outside the workspace", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeSession}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		c.handleNotification("item/started", started("/repo/facet/src/ListTable.tsx", "delete"))

		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, approval))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]string{"decision": "acceptForSession"}))
		request := recorder.received()[0]
		Expect(request.Tool).To(Equal("apply_patch"))
		Expect(request.ToolUseID).To(Equal("item-5"))
		Expect(request.Kind).To(Equal(api.ApprovalKindFilesystem))
		Expect(request.Escalates).To(BeTrue())
		Expect(request.Interruptible).To(BeTrue())
		Expect(request.SupportedScopes).To(Equal([]api.ApprovalScope{api.ApprovalScopeRequest, api.ApprovalScopeSession}))
		Expect(request.Filesystem).To(Equal(&api.FilesystemApproval{
			Operation: api.FilesystemPatch, Paths: []string{"/repo/facet/src/ListTable.tsx"},
			Changes: []api.FileChange{{Path: "/repo/facet/src/ListTable.tsx", Kind: "delete"}},
		}))
		Expect(request.Input).To(HaveKeyWithValue("changes", ContainElement(HaveKeyWithValue("diff", "-old\n+new"))))
	})

	It("reads the latest patch and does not flag a write inside the workspace", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: false}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		c.handleNotification("item/started", started("/repo/facet/a.go", "add"))
		c.handleNotification("item/fileChange/patchUpdated", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-5",
			"changes":[{"path":"/repo/gavel/a.go","kind":{"type":"update","move_path":"/repo/gavel/b.go"},"diff":""}]}`))

		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, method, approval))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]string{"decision": "decline"}))
		request := recorder.received()[0]
		Expect(request.Escalates).To(BeFalse())
		Expect(request.Filesystem.Paths).To(Equal([]string{"/repo/gavel/a.go", "/repo/gavel/b.go"}))
	})

	It("fails loudly for an approval whose item never started", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		_, rpcErr := c.handleApproval(codexServerRequest(`1`, method, approval))
		Expect(rpcErr).NotTo(BeNil())
		Expect(rpcErr.Message).To(ContainSubstring(`unknown fileChange item "item-5"`))
		Expect(recorder.received()).To(BeEmpty())
	})
})
