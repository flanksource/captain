package provider

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex app-server questions", func() {
	question := json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"location","header":"Plan","question":"Where should the plan go?","options":[{"label":"Inline","description":"Store plan content"}],"isOther":true}]}`)

	It("passes a blocking question to the host and returns answers by id", func() {
		var received api.PermissionRequest
		c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}, CanUseTool: func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
			received = request
			return api.PermissionDecision{Allow: true, UpdatedInput: map[string]any{"answers": map[string]any{"location": "Inline"}}}, nil
		}})
		Expect(err).NotTo(HaveOccurred())
		turn := &turnState{ctx: context.Background(), terminal: make(chan struct{}), started: make(chan struct{})}
		turn.setIDs("thread-1", "turn-1")
		c.setActive(turn)

		answer, rpcErr := c.handleApproval("item/tool/requestUserInput", question)

		Expect(rpcErr).To(BeNil())
		Expect(received.Tool).To(Equal("AskUserQuestion"))
		Expect(received.ToolUseID).To(Equal("item-1"))
		Expect(received.Input).To(HaveKey("questions"))
		Expect(answer).To(Equal(map[string]any{"answers": map[string]any{"location": map[string]any{"answers": []string{"Inline"}}}}))
	})

	It("fails loudly without a question broker", func() {
		c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}})
		Expect(err).NotTo(HaveOccurred())
		turn := &turnState{ctx: context.Background(), terminal: make(chan struct{}), started: make(chan struct{})}
		turn.setIDs("thread-1", "turn-1")
		c.setActive(turn)

		_, rpcErr := c.handleApproval("item/tool/requestUserInput", question)
		Expect(rpcErr).NotTo(BeNil())
		Expect(rpcErr.Message).To(ContainSubstring("question broker"))
	})

	DescribeTable("rejects requests that cannot be safely delivered",
		func(raw json.RawMessage, expected string) {
			var called atomic.Bool
			c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}, CanUseTool: func(context.Context, api.PermissionRequest) (api.PermissionDecision, error) {
				called.Store(true)
				return api.PermissionDecision{}, nil
			}})
			Expect(err).NotTo(HaveOccurred())
			turn := &turnState{ctx: context.Background(), terminal: make(chan struct{}), started: make(chan struct{})}
			turn.setIDs("thread-1", "turn-1")
			c.setActive(turn)
			_, rpcErr := c.handleApproval("item/tool/requestUserInput", raw)
			Expect(rpcErr).NotTo(BeNil())
			Expect(rpcErr.Message).To(ContainSubstring(expected))
			Expect(called.Load()).To(BeFalse())
		},
		Entry("nonblocking", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":false,"questions":[{"id":"location","question":"Where?"}]}`), "nonblocking"),
		Entry("secret", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","isBlocking":true,"questions":[{"id":"location","question":"Where?","isSecret":true}]}`), "secret"),
		Entry("wrong turn", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-2","itemId":"item-1","isBlocking":true,"questions":[{"id":"location","question":"Where?"}]}`), "active thread"),
	)

	It("cancels the broker wait when the turn ends", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		waiting := make(chan struct{})
		c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.6"}, CanUseTool: func(ctx context.Context, _ api.PermissionRequest) (api.PermissionDecision, error) {
			close(waiting)
			<-ctx.Done()
			return api.PermissionDecision{}, ctx.Err()
		}})
		Expect(err).NotTo(HaveOccurred())
		turn := &turnState{ctx: ctx, terminal: make(chan struct{}), started: make(chan struct{})}
		turn.setIDs("thread-1", "turn-1")
		c.setActive(turn)
		finished := make(chan *string, 1)
		go func() {
			_, rpcErr := c.handleApproval("item/tool/requestUserInput", question)
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
