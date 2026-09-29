package approval_test

import (
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

var _ = Describe("Typed approval requests through the durable broker", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	command := api.ApprovalRequest{
		Tool: "exec_command", Kind: api.ApprovalKindCommand, ToolUseID: "appr_rebase",
		Input:  map[string]any{"command": "git rebase --continue"},
		Reason: "May I continue the active rebase?", Escalates: true, Interruptible: true,
		SupportedScopes: []api.ApprovalScope{api.ApprovalScopeRequest, api.ApprovalScopeSession},
		Command:         &api.CommandApproval{Command: "git rebase --continue", ProposedPolicy: []string{"git", "rebase"}},
	}

	raise := func(ctx SpecContext, request api.ApprovalRequest) (*providerRun, chan outcome, uuid.UUID) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(time.Minute), request)
		return run, outcomes, uuid.MustParse(run.awaitPermission().ApprovalID)
	}

	It("persists the kind and payload beside the tool and input old readers use", func(ctx SpecContext) {
		_, _, id := raise(ctx, command)
		row, err := db.GetTurnRequest(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Kind).To(Equal("tool_approval"))
		Expect(row.Request).To(Equal(map[string]any{
			"tool": "exec_command", "input": map[string]any{"command": "git rebase --continue"},
			"kind": "command", "reason": "May I continue the active rebase?", "escalates": true, "interruptible": true,
			"supportedScopes": []any{"request", "session"},
			"command":         map[string]any{"command": "git rebase --continue", "proposedPolicy": []any{"git", "rebase"}},
		}))
	})

	It("hands a session-scoped approval back to the provider", func(ctx SpecContext) {
		run, outcomes, id := raise(ctx, command)
		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: id, SessionID: run.session, Approved: true, Scope: api.ApprovalScopeSession, ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes).Should(Receive(Equal(outcome{decision: api.ApprovalDecision{Allow: true, Scope: api.ApprovalScopeSession}})))
	})

	It("hands a cancel back as a deny that interrupts the turn", func(ctx SpecContext) {
		run, outcomes, id := raise(ctx, command)
		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: id, SessionID: run.session, Interrupt: true, Reason: "stop here", ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes).Should(Receive(Equal(outcome{decision: api.ApprovalDecision{Message: "stop here", Interrupt: true}})))
	})

	It("hands granted permissions back to the provider", func(ctx SpecContext) {
		permissions := api.ApprovalRequest{
			Tool: "request_permissions", Kind: api.ApprovalKindPermissions, ToolUseID: "item_perm",
			SupportedScopes: []api.ApprovalScope{api.ApprovalScopeTurn},
			Permissions: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/.git"}},
				Network:    &api.SandboxNetworkPolicy{Access: api.SandboxNetworkUnrestricted},
			},
		}
		grant := &api.NativeSandboxPolicy{Filesystem: &api.SandboxFilesystemPolicy{WritableRoots: []string{"/repo/.git"}}}
		run, outcomes, id := raise(ctx, permissions)
		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: id, SessionID: run.session, Approved: true, Scope: api.ApprovalScopeTurn, Grants: grant, ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes).Should(Receive(Equal(outcome{decision: api.ApprovalDecision{
			Allow: true, Scope: api.ApprovalScopeTurn, Grants: grant,
		}})))
	})

	It("refuses a decision the stored request cannot take and leaves the row pending", func(ctx SpecContext) {
		run, outcomes, id := raise(ctx, command)
		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: id, SessionID: run.session, Approved: true, Scope: api.ApprovalScopeTurn, ResolvedBy: "dashboard",
		})
		Expect(err).To(MatchError(approval.ErrInvalidResolution))
		Expect(err).To(MatchError(ContainSubstring(`scope "turn"`)))
		row, err := db.GetTurnRequest(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.State).To(Equal(database.TurnRequestStatePending))
		Consistently(outcomes, 5*brokerPoll).ShouldNot(Receive())
	})

	It("keeps a command and a question pending on one run and answers each on its own", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		question := api.ApprovalRequest{
			Tool: "AskUserQuestion", Kind: api.ApprovalKindQuestion, ToolUseID: "item_q",
			Questions: []api.TerminalQuestion{{ID: "scope", Text: "How far?", Options: []string{"Phase 1", "All"}}},
		}
		commands := run.callTool(ctx, broker, command)
		first := uuid.MustParse(run.awaitPermission().ApprovalID)
		questions := run.callTool(ctx, broker, question)
		second := uuid.MustParse(run.awaitPermission().ApprovalID)
		Expect(first).NotTo(Equal(second))

		answers := map[string]any{"answers": map[string]any{"scope": "Phase 1"}}
		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: second, SessionID: run.session, Approved: true, UpdatedInput: answers, ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(questions).Should(Receive(Equal(outcome{decision: api.ApprovalDecision{Allow: true, UpdatedInput: answers}})))
		Consistently(commands, 5*brokerPoll).ShouldNot(Receive())
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateWaiting))

		_, err = approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: first, SessionID: run.session, Reason: "not now", ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(commands).Should(Receive(Equal(outcome{decision: api.ApprovalDecision{Message: "not now"}})))
		Eventually(func() database.PromptRunState { return run.state(ctx) }).Should(Equal(database.PromptRunStateRunning))
	})

	It("records two approvals raised by one Codex item as two requests", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		network := command
		network.ToolUseID, network.Kind, network.Command = "appr_network", api.ApprovalKindNetwork, nil
		network.Network = &api.NetworkApproval{Host: "api.github.com", Protocol: "https", Command: "git rebase --continue"}
		run.callTool(ctx, broker, command)
		first := run.awaitPermission().ApprovalID
		run.callTool(ctx, broker, network)
		second := run.awaitPermission().ApprovalID
		Expect(second).NotTo(Equal(first), "a second approval on the same item must not replay the first")
	})

	It("still resolves a row written before kinds existed as a tool approval", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		row, err := db.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
			SessionID: run.session, PromptRunID: run.run, ToolCallID: "toolu_old", Tool: "Edit",
			Input: map[string]any{"file_path": "a.go"}, RequestedBy: "provider", ExpiresAt: time.Now().Add(time.Minute),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Request).To(Equal(map[string]any{"tool": "Edit", "input": map[string]any{"file_path": "a.go"}}))
		_, _, err = db.HoldPromptRunForApprovals(ctx, run.run)
		Expect(err).NotTo(HaveOccurred())
		_, err = approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: row.ID, SessionID: run.session, Approved: true,
			UpdatedInput: map[string]any{"file_path": "b.go"}, ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
	})
})
