package provider

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex app-server questions", func() {
	question := json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"location","header":"Plan","question":"Where should the plan go?","options":[{"label":"Inline","description":"Store plan content"}],"isOther":true}]}`)

	It("passes a blocking question to the host and returns answers by id", func() {
		var received api.ApprovalRequest
		c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}, OnApproval: func(_ context.Context, request api.ApprovalRequest) (api.ApprovalDecision, error) {
			received = request
			return api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"answers": map[string]any{"location": "Inline"}}}, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		turn := &turnState{ctx: context.Background(), terminal: make(chan struct{}), started: make(chan struct{})}
		turn.setIDs("thread-1", "turn-1")
		c.setActive(turn)

		answer, rpcErr := c.handleApproval(jsonrpc.ServerRequest{ID: json.RawMessage(`1`), Method: "item/tool/requestUserInput", Params: question})

		Expect(rpcErr).To(BeNil())
		Expect(received.Tool).To(Equal("AskUserQuestion"))
		Expect(received.ToolUseID).To(Equal("item-1"))
		Expect(received.Input).To(HaveKey("questions"))
		Expect(answer).To(Equal(map[string]any{"answers": map[string]any{"location": map[string]any{"answers": []string{"Inline"}}}}))
	})

	It("passes a secret question through for the broker to judge", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"answers": map[string]any{"token": "s3cr3t"}}}}
		c, _ := codexApprovalHarness(recorder.approve, workspaceRun)
		secret := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-2","isBlocking":true,"questions":[{"id":"token","question":"Paste the deploy token","isSecret":true}]}`

		answer, rpcErr := c.handleApproval(codexServerRequest(`1`, "item/tool/requestUserInput", secret))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]any{"answers": map[string]any{"token": map[string]any{"answers": []string{"s3cr3t"}}}}))
		Expect(recorder.received()[0].Questions).To(Equal([]api.TerminalQuestion{{ID: "token", Text: "Paste the deploy token", MultiSelect: true, Secret: true}}))
		Expect(recorder.received()[0].LegacyContract).To(BeTrue())
	})

	It("refuses a question the run's tool policy denies without asking", func() {
		recorder := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
		run := workspaceRun
		run.ToolPreferences = api.ToolPreferences{"AskUserQuestion": api.ToolPolicyDeny}
		c, _ := codexApprovalHarness(recorder.approve, run)

		_, rpcErr := c.handleApproval(codexServerRequest(`1`, "item/tool/requestUserInput", string(question)))

		Expect(rpcErr).NotTo(BeNil())
		Expect(rpcErr.Message).To(ContainSubstring("denied by the run's permission policy"))
		Expect(recorder.received()).To(BeEmpty())
	})

	It("fails loudly without a question broker", func() {
		c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}})
		Expect(err).NotTo(HaveOccurred())
		turn := &turnState{ctx: context.Background(), terminal: make(chan struct{}), started: make(chan struct{})}
		turn.setIDs("thread-1", "turn-1")
		c.setActive(turn)

		_, rpcErr := c.handleApproval(jsonrpc.ServerRequest{ID: json.RawMessage(`1`), Method: "item/tool/requestUserInput", Params: question})
		Expect(rpcErr).NotTo(BeNil())
		Expect(rpcErr.Message).To(ContainSubstring("question broker"))
	})

	DescribeTable("rejects requests that cannot be safely delivered",
		func(raw json.RawMessage, expected string) {
			var called atomic.Bool
			c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}, OnApproval: func(context.Context, api.ApprovalRequest) (api.ApprovalDecision, error) {
				called.Store(true)
				return api.ApprovalDecision{}, nil
			}})
			Expect(err).NotTo(HaveOccurred())
			turn := &turnState{ctx: context.Background(), terminal: make(chan struct{}), started: make(chan struct{})}
			turn.setIDs("thread-1", "turn-1")
			c.setActive(turn)
			_, rpcErr := c.handleApproval(jsonrpc.ServerRequest{ID: json.RawMessage(`1`), Method: "item/tool/requestUserInput", Params: raw})
			Expect(rpcErr).NotTo(BeNil())
			Expect(rpcErr.Message).To(ContainSubstring(expected))
			Expect(called.Load()).To(BeFalse())
		},
		Entry("nonblocking", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":false,"questions":[{"id":"location","question":"Where?"}]}`), "nonblocking"),
		Entry("wrong turn", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-2","itemId":"item-1","isBlocking":true,"questions":[{"id":"location","question":"Where?"}]}`), "active thread"),
	)

	It("cancels the broker wait when the turn ends", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		waiting := make(chan struct{})
		c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}, OnApproval: func(ctx context.Context, _ api.ApprovalRequest) (api.ApprovalDecision, error) {
			close(waiting)
			<-ctx.Done()
			return api.ApprovalDecision{}, ctx.Err()
		}})
		Expect(err).NotTo(HaveOccurred())
		turn := &turnState{ctx: ctx, terminal: make(chan struct{}), started: make(chan struct{})}
		turn.setIDs("thread-1", "turn-1")
		c.setActive(turn)
		finished := make(chan *string, 1)
		go func() {
			_, rpcErr := c.handleApproval(jsonrpc.ServerRequest{ID: json.RawMessage(`1`), Method: "item/tool/requestUserInput", Params: question})
			if rpcErr == nil {
				finished <- nil
				return
			}
			finished <- &rpcErr.Message
		}()
		<-waiting
		cancel()
		message := <-finished
		Expect(message).NotTo(BeNil())
		Expect(*message).To(ContainSubstring("canceled"))
	})
})
