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
	It("emits one command use and one correlated result", func() {
		client, turn := activeGinkgoTurn()
		client.handleNotification("item/started", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-1","type":"commandExecution","command":"pwd","cwd":"/work","status":"inProgress"}
		}`))
		client.handleNotification("item/commandExecution/outputDelta", json.RawMessage(`{
			"threadId":"thread-1","itemId":"cmd-1","delta":"/work\n"
		}`))
		client.handleNotification("item/completed", json.RawMessage(`{
			"threadId":"thread-1",
			"item":{"id":"cmd-1","type":"commandExecution","command":"pwd","cwd":"/work","status":"completed","exitCode":0,"aggregatedOutput":""}
		}`))

		events := drainEvents(turn)
		Expect(events).To(HaveLen(2))
		Expect(events[0]).To(MatchFields(IgnoreExtras, Fields{
			"Kind":       Equal(ai.EventToolUse),
			"Tool":       Equal("Bash"),
			"Input":      Equal(map[string]any{"command": "pwd"}),
			"ToolCallID": Equal("cmd-1"),
			"SessionID":  Equal("thread-1"),
		}))
		Expect(events[1]).To(MatchFields(IgnoreExtras, Fields{
			"Kind":       Equal(ai.EventToolResult),
			"Text":       Equal("/work\n"),
			"ToolCallID": Equal("cmd-1"),
			"SessionID":  Equal("thread-1"),
			"Success":    BeTrue(),
		}))

		tool, ok := events[0].Raw.(claude.ToolUse)
		Expect(ok).To(BeTrue())
		Expect(tool.ToolUseID).To(Equal("cmd-1"))
		Expect(tool.SessionID).To(Equal("thread-1"))
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

		events := drainEvents(turn)
		Expect(events).To(HaveLen(2))
		Expect(events[1].Kind).To(Equal(ai.EventToolResult))
		Expect(events[1].Success).To(BeFalse())
		Expect(events[1].Text).To(Equal("failed\n"))
	})
})

var _ = Describe("Codex app-server turn control", func() {
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
		streamed:   map[string]bool{},
		toolOutput: map[string]string{},
		terminal:   make(chan struct{}),
	}
	client.setActive(turn)
	return client, turn
}
