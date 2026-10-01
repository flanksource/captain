package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex app-server MCP elicitation", func() {
	const method = "mcpServer/elicitation/request"
	const form = `{"threadId":"thread-1","turnId":"turn-1","serverName":"github","mode":"form",
		"message":"Which repository should the issue be filed in?",
		"requestedSchema":{"type":"object","properties":{"repo":{"type":"string","enum":["flanksource/captain","flanksource/gavel"]},"labels":{"type":"string"}},"required":["repo"]}}`
	const url = `{"threadId":"thread-1","turnId":null,"serverName":"linear","mode":"url","elicitationId":"el-1",
		"message":"Authorize access to your workspace","url":"https://linear.example/oauth/authorize"}`
	decline := map[string]any{"action": "decline"}

	It("asks for form content keyed by the JSON-RPC request id", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"repo": "flanksource/captain"}}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)

		answer, rpcErr := c.handleApproval(codexServerRequest(`7`, method, form))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]any{"action": "accept", "content": map[string]any{"repo": "flanksource/captain"}}))
		Expect(recorder.received()).To(Equal([]api.ApprovalRequest{{
			Tool: "Elicitation", Input: rawInput(form), ToolUseID: "elicit:7", SessionID: "thread-1", TurnID: "turn-1",
			Kind: api.ApprovalKindElicitation, Interruptible: true, SupportedScopes: []api.ApprovalScope{api.ApprovalScopeRequest},
			Info: &api.ToolInfo{Name: "Elicitation", Parent: "github"},
			Elicitation: &api.ElicitationApproval{
				Server: "github", Mode: api.ElicitationModeForm, Message: "Which repository should the issue be filed in?",
				Schema: rawInput(form)["requestedSchema"].(map[string]any),
			},
		}}))
	})

	It("completes a url-mode elicitation without content", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)

		answer, rpcErr := c.handleApproval(codexServerRequest(`"req-8"`, method, url))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]any{"action": "accept"}))
		request := recorder.received()[0]
		Expect(request.ToolUseID).To(Equal("elicit:req-8"))
		Expect(request.Elicitation).To(Equal(&api.ElicitationApproval{
			Server: "linear", Mode: api.ElicitationModeURL, Message: "Authorize access to your workspace",
			URL: "https://linear.example/oauth/authorize", ElicitationID: "el-1",
		}))
	})

	DescribeTable("translates a refusal",
		func(decision api.ApprovalDecision, want string) {
			recorder := &approvalRecorder{decision: decision}
			c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
			answer, rpcErr := c.handleApproval(codexServerRequest(`7`, method, form))
			Expect(rpcErr).To(BeNil())
			Expect(answer).To(Equal(map[string]any{"action": want}))
		},
		Entry("deny", api.ApprovalDecision{Allow: false}, "decline"),
		Entry("cancel", api.ApprovalDecision{Allow: false, Interrupt: true}, "cancel"),
	)

	DescribeTable("refuses content the elicitation cannot take",
		func(params string, content map[string]any, wantErr string) {
			recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true, UpdatedInput: content}}
			c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
			_, rpcErr := c.handleApproval(codexServerRequest(`7`, method, params))
			Expect(rpcErr).NotTo(BeNil())
			Expect(rpcErr.Message).To(ContainSubstring(wantErr))
		},
		Entry("form content missing a required field", form, map[string]any{"labels": "bug"}, `required field "repo" is missing`),
		Entry("form content outside the enum", form, map[string]any{"repo": "acme/other"}, "is not one of"),
		Entry("content on a url-mode elicitation", url, map[string]any{"done": true}, "takes no content"),
	)

	It("treats the openai form modes as forms", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"ok": true}}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		params := `{"threadId":"thread-1","serverName":"acme","mode":"openai/form","message":"Confirm?",
			"requestedSchema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}`

		answer, rpcErr := c.handleApproval(codexServerRequest(`9`, method, params))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]any{"action": "accept", "content": map[string]any{"ok": true}}))
		Expect(recorder.received()[0].Elicitation.Mode).To(Equal(api.ElicitationModeForm))
	})

	It("refuses a device verification it cannot answer", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		params := `{"threadId":"thread-1","serverName":"acme","mode":"openai/userVerification","title":"t","description":"d","challenge":"c"}`
		_, rpcErr := c.handleApproval(codexServerRequest(`9`, method, params))
		Expect(rpcErr).NotTo(BeNil())
		Expect(rpcErr.Message).To(ContainSubstring(`mode "openai/userVerification"`))
		Expect(recorder.received()).To(BeEmpty())
	})

	It("declines without a callback, for a legacy callback, and in plan mode", func() {
		c, _ := codexApprovalHarness(nil, workspaceRun)
		answer, rpcErr := c.handleApproval(codexServerRequest(`7`, method, form))
		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(decline))

		legacy := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		c, _ = codexApprovalHarness(api.LegacyApprovalFunc(legacy.approve, "Config.CanUseTool", ""), workspaceRun)
		answer, rpcErr = c.handleApproval(codexServerRequest(`7`, method, form))
		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(decline))

		plan := workspaceRun
		plan.Permissions.Mode = api.PermissionPlan
		c, _ = codexApprovalHarness(legacy.approve, plan)
		answer, rpcErr = c.handleApproval(codexServerRequest(`7`, method, form))
		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(decline))
		Expect(legacy.received()).To(BeEmpty())
	})

	It("cancels an elicitation still pending when the turn ends", func() {
		ctx, cancel := context.WithCancel(context.Background())
		waiting := make(chan struct{})
		c, turn := codexApprovalHarness(func(ctx context.Context, _ api.ApprovalRequest) (api.ApprovalDecision, error) {
			close(waiting)
			<-ctx.Done()
			return api.ApprovalDecision{}, ctx.Err()
		}, workspaceRun)
		turn.ctx = ctx
		answers := make(chan any, 1)
		go func() {
			answer, _ := c.handleApproval(codexServerRequest(`7`, method, form))
			answers <- answer
		}()
		Eventually(waiting).Should(BeClosed())
		cancel()
		Eventually(answers).Should(Receive(Equal(map[string]any{"action": "cancel"})))
	})

	It("keeps two concurrent elicitations on one run apart", func() {
		var mu sync.Mutex
		seen := map[string]bool{}
		both := make(chan struct{})
		c, _ := codexApprovalHarness(func(_ context.Context, request api.ApprovalRequest) (api.ApprovalDecision, error) {
			mu.Lock()
			seen[request.ToolUseID] = true
			arrived := len(seen)
			if arrived == 2 {
				close(both)
			}
			mu.Unlock()
			select {
			case <-both:
			case <-time.After(5 * time.Second):
				return api.ApprovalDecision{}, fmt.Errorf("only %d elicitations arrived together", arrived)
			}
			return api.ApprovalDecision{Allow: request.ToolUseID == "elicit:1", UpdatedInput: map[string]any{"repo": "flanksource/gavel"}}, nil
		}, workspaceRun)
		answers := make(chan any, 2)
		for _, id := range []string{`1`, `2`} {
			go func() {
				answer, rpcErr := c.handleApproval(codexServerRequest(id, method, form))
				if rpcErr != nil {
					answers <- rpcErr.Message
					return
				}
				answers <- answer
			}()
		}
		var first, second any
		Eventually(answers).Should(Receive(&first))
		Eventually(answers).Should(Receive(&second))
		Expect([]any{first, second}).To(ConsistOf(
			map[string]any{"action": "accept", "content": map[string]any{"repo": "flanksource/gavel"}},
			map[string]any{"action": "decline"},
		))
	})
})
