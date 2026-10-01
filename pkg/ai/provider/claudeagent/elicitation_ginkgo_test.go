package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	formElicitation = `{"bridgeId":"bridge-a","requestId":7,"serverName":"github","mode":"form",
		"message":"Which repository should the issue be filed in?",
		"requestedSchema":{"type":"object","properties":{"repo":{"type":"string","enum":["flanksource/captain","flanksource/gavel"]},"labels":{"type":"string"}},"required":["repo"]}}`
	urlElicitation = `{"bridgeId":"bridge-a","requestId":8,"serverName":"linear","mode":"url","elicitationId":"el-1",
		"message":"Authorize access to your workspace","url":"https://linear.example/oauth/authorize"}`
)

func elicitingProvider(ctx context.Context, callback ai.ApprovalFunc) *Provider {
	provider := &Provider{model: testModel, baseCtx: context.Background(), cfg: ai.Config{OnApproval: callback}}
	provider.setActive(&turnState{
		ctx: ctx, inbox: make(chan ai.Event, 4), term: make(chan struct{}), quit: make(chan struct{}),
		onApproval: callback,
	})
	return provider
}

func elicit(provider *Provider, params string) elicitResult {
	raw, rpcErr := provider.onRequest(jsonrpc.ServerRequest{Method: methodElicit, Params: json.RawMessage(params)})
	Expect(rpcErr).To(BeNil())
	result, ok := raw.(elicitResult)
	Expect(ok).To(BeTrue())
	return result
}

func answering(decision ai.ApprovalDecision, err error, calls *[]ai.ApprovalRequest) ai.ApprovalFunc {
	return func(_ context.Context, request ai.ApprovalRequest) (ai.ApprovalDecision, error) {
		*calls = append(*calls, request)
		return decision, err
	}
}

var _ = Describe("Claude Agent elicitation", func() {
	It("asks the callback with an elicitation request keyed by bridge instance and request id", func() {
		var calls []ai.ApprovalRequest
		provider := elicitingProvider(context.Background(), answering(ai.ApprovalDecision{Allow: false}, nil, &calls))

		elicit(provider, formElicitation)

		Expect(calls).To(HaveLen(1))
		request := calls[0]
		Expect(request.Input).To(HaveKeyWithValue("serverName", "github"))
		request.Input = nil
		Expect(request).To(Equal(ai.ApprovalRequest{
			Tool: "Elicitation", ToolUseID: "elicit:bridge-a:7",
			Kind: api.ApprovalKindElicitation, Interruptible: true,
			SupportedScopes: []api.ApprovalScope{api.ApprovalScopeRequest},
			Elicitation: &api.ElicitationApproval{
				Server: "github", Mode: api.ElicitationModeForm,
				Message: "Which repository should the issue be filed in?",
				Schema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo":   map[string]any{"type": "string", "enum": []any{"flanksource/captain", "flanksource/gavel"}},
						"labels": map[string]any{"type": "string"},
					},
					"required": []any{"repo"},
				},
			},
		}))
	})

	DescribeTable("translates the decision into the MCP elicitation result",
		func(params string, decision ai.ApprovalDecision, expected elicitResult) {
			var calls []ai.ApprovalRequest
			provider := elicitingProvider(context.Background(), answering(decision, nil, &calls))

			Expect(elicit(provider, params)).To(Equal(expected))
		},
		Entry("form content that fits the schema is accepted", formElicitation,
			ai.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"repo": "flanksource/captain"}},
			elicitResult{Action: elicitAccept, Content: map[string]any{"repo": "flanksource/captain"}}),
		Entry("form content missing a required field is cancelled", formElicitation,
			ai.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"labels": "bug"}},
			elicitResult{Action: elicitCancel}),
		Entry("a finished url flow is accepted with no content", urlElicitation,
			ai.ApprovalDecision{Allow: true}, elicitResult{Action: elicitAccept}),
		Entry("content on a url flow is cancelled", urlElicitation,
			ai.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"token": "x"}}, elicitResult{Action: elicitCancel}),
		Entry("a deny declines", formElicitation, ai.ApprovalDecision{Allow: false}, elicitResult{Action: elicitDecline}),
		Entry("a deny that interrupts cancels", formElicitation,
			ai.ApprovalDecision{Allow: false, Interrupt: true}, elicitResult{Action: elicitCancel}),
	)

	It("serialises an accept as the MCP result shape", func() {
		wire, err := json.Marshal(elicitResult{Action: elicitAccept, Content: map[string]any{"repo": "flanksource/captain"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(wire)).To(MatchJSON(`{"action":"accept","content":{"repo":"flanksource/captain"}}`))
	})

	It("declines when no callback brokers approvals", func() {
		provider := &Provider{model: testModel, baseCtx: context.Background()}

		Expect(elicit(provider, formElicitation)).To(Equal(elicitResult{Action: elicitDecline}))
	})

	It("cancels when no turn is active", func() {
		var calls []ai.ApprovalRequest
		provider := &Provider{model: testModel, baseCtx: context.Background(), cfg: ai.Config{
			OnApproval: answering(ai.ApprovalDecision{Allow: true}, nil, &calls),
		}}

		Expect(elicit(provider, formElicitation)).To(Equal(elicitResult{Action: elicitCancel}))
		Expect(calls).To(BeEmpty())
	})

	It("declines, as with no callback, when a legacy callback skips the elicitation", func() {
		var calls []ai.ApprovalRequest
		legacy := api.LegacyApprovalFunc(answering(ai.ApprovalDecision{Allow: true}, nil, &calls), "Config.CanUseTool", "")
		provider := elicitingProvider(context.Background(), legacy)

		Expect(elicit(provider, formElicitation)).To(Equal(elicitResult{Action: elicitDecline}))
		Expect(calls).To(BeEmpty())
	})

	It("cancels when the callback fails", func() {
		var calls []ai.ApprovalRequest
		provider := elicitingProvider(context.Background(), answering(ai.ApprovalDecision{}, errors.New("broker unavailable"), &calls))

		Expect(elicit(provider, formElicitation)).To(Equal(elicitResult{Action: elicitCancel}))
		Expect(calls).To(HaveLen(1))
	})

	It("cancels when the turn ends while the elicitation is pending", func() {
		ctx, cancel := context.WithCancel(context.Background())
		pending := make(chan struct{})
		provider := elicitingProvider(ctx, func(ctx context.Context, _ ai.ApprovalRequest) (ai.ApprovalDecision, error) {
			close(pending)
			<-ctx.Done()
			return ai.ApprovalDecision{}, ctx.Err()
		})
		go func() {
			<-pending
			cancel()
		}()

		Expect(elicit(provider, formElicitation)).To(Equal(elicitResult{Action: elicitCancel}))
	})

	It("answers two concurrent elicitations on one run independently", func() {
		var (
			mu      sync.Mutex
			waiting = map[string]chan struct{}{}
			arrived = make(chan string, 2)
		)
		provider := elicitingProvider(context.Background(), func(_ context.Context, request ai.ApprovalRequest) (ai.ApprovalDecision, error) {
			release := make(chan struct{})
			mu.Lock()
			waiting[request.ToolUseID] = release
			mu.Unlock()
			arrived <- request.ToolUseID
			<-release
			if request.Elicitation.Mode == api.ElicitationModeURL {
				return ai.ApprovalDecision{Allow: true}, nil
			}
			return ai.ApprovalDecision{Allow: false}, nil
		})

		results := make(chan elicitResult, 2)
		for _, params := range []string{formElicitation, urlElicitation} {
			go func(params string) {
				defer GinkgoRecover()
				results <- elicit(provider, params)
			}(params)
		}
		Eventually(arrived).Should(Receive())
		Eventually(arrived).Should(Receive())
		mu.Lock()
		Expect(waiting).To(HaveKey("elicit:bridge-a:7"))
		Expect(waiting).To(HaveKey("elicit:bridge-a:8"))
		for _, release := range waiting {
			close(release)
		}
		mu.Unlock()

		var first, second elicitResult
		Eventually(results).Should(Receive(&first))
		Eventually(results).Should(Receive(&second))
		Expect([]elicitResult{first, second}).To(ConsistOf(elicitResult{Action: elicitAccept}, elicitResult{Action: elicitDecline}))
	})

	DescribeTable("rejects bridge params it cannot turn into a request",
		func(params, reason string) {
			provider := elicitingProvider(context.Background(), answering(ai.ApprovalDecision{Allow: true}, nil, &[]ai.ApprovalRequest{}))

			_, rpcErr := provider.onRequest(jsonrpc.ServerRequest{Method: methodElicit, Params: json.RawMessage(params)})

			Expect(rpcErr).NotTo(BeNil())
			Expect(rpcErr.Code).To(Equal(-32602))
			Expect(rpcErr.Message).To(ContainSubstring(reason))
		},
		Entry("an unknown mode", `{"bridgeId":"b","requestId":1,"serverName":"s","message":"m","mode":"popup"}`, `mode "popup"`),
		Entry("no bridge instance", `{"requestId":1,"serverName":"s","message":"m","mode":"form"}`, "bridgeId"),
		Entry("no request id", `{"bridgeId":"b","serverName":"s","message":"m","mode":"form"}`, "requestId"),
		Entry("no server", `{"bridgeId":"b","requestId":1,"message":"m","mode":"form"}`, "serverName"),
	)
})
