package provider

import (
	"context"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex app-server permission mode", func() {
	commandApproval := func(c *CodexAppServer) string {
		answer, rpcErr := c.handleApproval("item/commandExecution/requestApproval", nil)
		Expect(rpcErr).To(BeNil())
		return answer.(map[string]string)["decision"]
	}

	DescribeTable("names the approvals reviewer on thread start, resume, and every turn",
		func(mode api.PermissionMode, wantReviewer string) {
			request := ai.Request{Prompt: api.Prompt{User: "inspect"}, Permissions: api.Permissions{Mode: mode}}

			start, err := buildThreadStartParams("gpt-5.6", request, nil)
			Expect(err).NotTo(HaveOccurred())
			resume, err := buildResumeParams(request, nil)
			Expect(err).NotTo(HaveOccurred())
			turn, err := buildTurnStartParams("gpt-5.6", request, "thread-1", nil)
			Expect(err).NotTo(HaveOccurred())

			for _, params := range []map[string]any{start, resume, turn} {
				if wantReviewer == "" {
					Expect(params).NotTo(HaveKey("approvalsReviewer"))
				} else {
					Expect(params).To(HaveKeyWithValue("approvalsReviewer", wantReviewer))
				}
			}
		},
		Entry("auto routes approvals to the auto-review subagent", api.PermissionAuto, "auto_review"),
		Entry("default routes approvals back to the user", api.PermissionDefault, "user"),
		Entry("an unstated posture leaves the user's Codex configuration alone", api.PermissionMode(""), ""),
	)

	DescribeTable("sets the collaboration mode on every turn",
		func(permission api.PermissionMode, want string) {
			request := ai.Request{Model: api.Model{Effort: api.EffortHigh}, Prompt: api.Prompt{User: "inspect"}, Permissions: api.Permissions{Mode: permission}}
			turn, err := buildTurnStartParams("gpt-5.6", request, "thread-1", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(turn["collaborationMode"]).To(Equal(map[string]any{
				"mode":     want,
				"settings": map[string]any{"model": "gpt-5.6", "reasoning_effort": "high", "developer_instructions": nil},
			}))
		},
		Entry("plan", api.PermissionPlan, "plan"),
		Entry("default after plan", api.PermissionDefault, "default"),
	)

	It("answers approvals under a switched posture at once and carries it into the next turn", func() {
		c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}})
		Expect(err).NotTo(HaveOccurred())
		c.beginTurn(ai.Request{Permissions: api.Permissions{Mode: api.PermissionBypass}})
		Expect(commandApproval(c)).To(Equal("decline"))

		Expect(c.SetPermissionMode(context.Background(), api.PermissionPlan)).To(Succeed())

		Expect(commandApproval(c)).To(Equal("decline"))
		c.turnMu.Unlock()
		next := c.beginTurn(ai.Request{Permissions: api.Permissions{Mode: api.PermissionBypass}})
		c.turnMu.Unlock()
		Expect(next.Permissions.Mode).To(Equal(api.PermissionPlan))
		Expect(commandApproval(c)).To(Equal("decline"))
	})

	It("sends a switch to auto on the next turn/start", func() {
		c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}})
		Expect(err).NotTo(HaveOccurred())

		Expect(c.SetPermissionMode(context.Background(), api.PermissionAuto)).To(Succeed())
		next := c.beginTurn(ai.Request{Prompt: api.Prompt{User: "continue"}, Permissions: api.Permissions{Mode: api.PermissionDefault}})
		c.turnMu.Unlock()
		turn, err := buildTurnStartParams("gpt-5.6", next, "thread-1", nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(turn).To(HaveKeyWithValue("approvalPolicy", "on-request"))
		Expect(turn).To(HaveKeyWithValue("approvalsReviewer", "auto_review"))
	})

	DescribeTable("refuses a posture the thread cannot take",
		func(sandbox *api.SandboxRef, mode api.PermissionMode, wantErr string) {
			c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}})
			Expect(err).NotTo(HaveOccurred())
			c.beginTurn(ai.Request{Sandbox: sandbox, Permissions: api.Permissions{Mode: api.PermissionDefault}})
			c.turnMu.Unlock()
			before := commandApproval(c)

			Expect(c.SetPermissionMode(context.Background(), mode)).To(MatchError(ContainSubstring(wantErr)))
			Expect(commandApproval(c)).To(Equal(before))
			next := c.beginTurn(ai.Request{Sandbox: sandbox, Permissions: api.Permissions{Mode: api.PermissionDefault}})
			c.turnMu.Unlock()
			Expect(next.Permissions.Mode).To(Equal(api.PermissionDefault))
		},
		Entry("dontAsk, which codex cannot express", nil, api.PermissionDontAsk, `"dontAsk" is not supported`),
		Entry("plan inside a docker sandbox", &api.SandboxRef{Mode: api.SandboxDocker}, api.PermissionPlan, "plan is not supported"),
		Entry("plan with disabled sandbox", &api.SandboxRef{Mode: api.SandboxOff}, api.PermissionPlan, "plan requires a read-only sandbox"),
		Entry("no posture at all", nil, api.PermissionMode(""), "permission mode is required"),
	)
})
