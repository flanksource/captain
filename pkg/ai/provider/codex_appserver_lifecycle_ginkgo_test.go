package provider

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

func TestCodexAppServerLifecycle(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Codex App Server Lifecycle Suite")
}

var _ = Describe("Codex app-server tool lifecycle", func() {
	It("emits one command use and one complete correlated result", func() {
		client, turn := activeGinkgoTurn()
		client.handleNotification("item/started", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-1","type":"commandExecution","command":"printf lines","cwd":"/work","status":"inProgress"}
		}`))
		client.handleNotification("item/commandExecution/outputDelta", json.RawMessage(`{
			"threadId":"thread-1","itemId":"cmd-1","delta":"line 2\nline 3\n"
		}`))
		client.handleNotification("item/completed", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-1","type":"commandExecution","command":"printf lines","cwd":"/work","status":"completed","exitCode":0,"aggregatedOutput":"line 2\nline 3\n"}
		}`))

		started := drainEvents(turn)
		Expect(started).To(HaveLen(1), "the partial item result waits for the raw function output")
		Expect(started[0]).To(MatchFields(IgnoreExtras, Fields{
			"Kind":       Equal(ai.EventToolUse),
			"Tool":       Equal("Bash"),
			"Input":      Equal(map[string]any{"command": "printf lines"}),
			"ToolCallID": Equal("cmd-1"),
			"SessionID":  Equal("thread-1"),
		}))
		client.handleNotification("rawResponseItem/completed", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"type":"function_call_output","call_id":"cmd-1","output":"Chunk ID: abc123\nWall time: 1 second\nProcess exited with code 0\nOutput:\nline 1\nline 2\nline 3\n"}
		}`))

		events := drainEvents(turn)
		Expect(events).To(HaveLen(1))
		Expect(events[0]).To(MatchFields(IgnoreExtras, Fields{
			"Kind":       Equal(ai.EventToolResult),
			"Text":       Equal("line 1\nline 2\nline 3\n"),
			"ToolCallID": Equal("cmd-1"),
			"SessionID":  Equal("thread-1"),
			"Success":    BeTrue(),
		}))

		tool, ok := events[0].Raw.(claude.ToolUse)
		Expect(ok).To(BeTrue())
		Expect(tool.ToolUseID).To(Equal("cmd-1"))
		Expect(tool.SessionID).To(Equal("thread-1"))
		Expect(tool.Response).To(Equal("line 1\nline 2\nline 3\n"))
	})

	It("falls back to the completed item when raw events are unavailable", func() {
		client, turn := activeGinkgoTurn()
		client.handleNotification("item/started", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-fallback","type":"commandExecution","command":"pwd","status":"inProgress"}
		}`))
		client.handleNotification("item/commandExecution/outputDelta", json.RawMessage(`{
			"threadId":"thread-1","itemId":"cmd-fallback","delta":"/wo"
		}`))
		client.handleNotification("item/commandExecution/outputDelta", json.RawMessage(`{
			"threadId":"thread-1","itemId":"cmd-fallback","delta":"rk\n"
		}`))
		client.handleNotification("item/completed", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-fallback","type":"commandExecution","command":"pwd","status":"completed","exitCode":0}
		}`))
		client.handleNotification("turn/completed", json.RawMessage(`{
			"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}
		}`))

		events := drainEvents(turn)
		Expect(events).To(HaveLen(3))
		Expect(events[0].Kind).To(Equal(ai.EventToolUse))
		Expect(events[1]).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal(ai.EventToolResult), "Text": Equal("/work\n"),
			"ToolCallID": Equal("cmd-fallback"), "Success": BeTrue(),
		}))
		Expect(events[2].Kind).To(Equal(ai.EventResult))
	})

	It("flushes a completed command before a transport terminal error", func() {
		client, turn := activeGinkgoTurn()
		client.handleNotification("item/started", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-exit","type":"commandExecution","command":"pwd","status":"inProgress"}
		}`))
		client.handleNotification("item/completed", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-exit","type":"commandExecution","command":"pwd","status":"completed","exitCode":0,"aggregatedOutput":"/work\n"}
		}`))

		turn.flushToolResults()
		turn.send(ai.Event{Kind: ai.EventError, Error: "codex app-server exited unexpectedly"})

		events := drainEvents(turn)
		Expect(events).To(HaveLen(3))
		Expect(events[1].Kind).To(Equal(ai.EventToolResult))
		Expect(events[1].ToolCallID).To(Equal("cmd-exit"))
		Expect(events[2].Kind).To(Equal(ai.EventError))
	})

	It("marks nonzero command completion as failed", func() {
		client, turn := activeGinkgoTurn()
		client.handleNotification("item/started", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-2","type":"commandExecution","command":"false","status":"inProgress"}
		}`))
		client.handleNotification("item/completed", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-2","type":"commandExecution","command":"false","status":"failed","exitCode":1,"aggregatedOutput":"failed\n"}
		}`))
		client.handleNotification("turn/completed", json.RawMessage(`{
			"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}
		}`))

		events := drainEvents(turn)
		Expect(events).To(HaveLen(3))
		Expect(events[1].Kind).To(Equal(ai.EventToolResult))
		Expect(events[1].Success).To(BeFalse())
		Expect(events[1].Text).To(Equal("failed\n"))
	})
})

var _ = Describe("Codex app-server turn control", func() {
	It("does not emit a successful result for an interrupted turn", func() {
		client, turn := activeGinkgoTurn()

		client.handleNotification("turn/completed", json.RawMessage(`{
			"threadId":"thread-1",
			"turn":{"id":"turn-1","status":"interrupted"}
		}`))

		Expect(drainEvents(turn)).To(BeEmpty())
		Eventually(turn.terminal).Should(BeClosed())
	})

	It("waits for thread and turn identifiers before interrupting", func() {
		turn := &turnState{terminal: make(chan struct{}), started: make(chan struct{})}
		go func() {
			time.Sleep(10 * time.Millisecond)
			turn.setIDs("thread-1", "turn-1")
		}()

		threadID, turnID, err := turn.waitIDs(context.Background())

		Expect(err).NotTo(HaveOccurred())
		Expect(threadID).To(Equal("thread-1"))
		Expect(turnID).To(Equal("turn-1"))
	})
})

var _ = Describe("Codex app-server attachments", func() {
	It("sends ordered localImage inputs after optional text", func() {
		attachment := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}.
			WithPreparedContent(api.AttachmentContent{Path: "/work/diagram.png"})
		params, err := buildTurnStartParams("gpt-5", ai.Request{Prompt: api.Prompt{
			User:        "inspect",
			Attachments: []api.AttachmentRef{attachment},
		}}, "thread-1", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(params["input"]).To(Equal([]map[string]any{
			{"type": "text", "text": "inspect"},
			{"type": "localImage", "path": "/work/diagram.png"},
		}))
	})
})

var _ = Describe("Codex CLI attachments", func() {
	It("adds one native --image argument per prepared attachment", func() {
		first := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/png"}.
			WithPreparedContent(api.AttachmentContent{Path: "/work/first.png"})
		second := api.AttachmentRef{ID: api.AttachmentIDPrefix + string(make([]byte, 64)), MediaType: "image/jpeg"}.
			WithPreparedContent(api.AttachmentContent{Path: "/work/second.jpg"})
		args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "codex"}, ai.Request{Prompt: api.Prompt{Attachments: []api.AttachmentRef{first, second}}})
		DeferCleanup(cleanup)
		Expect(err).NotTo(HaveOccurred())
		Expect(args[:6]).To(Equal([]string{"exec", "--json", "--image", "/work/first.png", "--image", "/work/second.jpg"}))
	})
})

func activeGinkgoTurn() (*CodexAppServer, *turnState) {
	client, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5"}})
	Expect(err).NotTo(HaveOccurred())
	turn := &turnState{
		ch:         make(chan ai.Event, 16),
		usage:      &ai.Usage{},
		model:      "gpt-5",
		streamed:   map[string]string{},
		toolOutput: map[string]string{},
		terminal:   make(chan struct{}),
	}
	client.setActive(turn)
	return client, turn
}
