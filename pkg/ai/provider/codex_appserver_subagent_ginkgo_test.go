package provider

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
)

var _ = Describe("Codex app-server sub-agent threads", func() {
	It("keeps a spawned sub-agent's notifications out of the run's own turn", func() {
		client, turn := activeGinkgoTurn()
		turn.setIDs("main-thread", "turn-1")

		client.handleNotification("thread/started", json.RawMessage(`{"thread":{"id":"sub-thread"}}`))
		client.handleNotification("item/agentMessage/delta", json.RawMessage(`{"threadId":"sub-thread","itemId":"m1","delta":"sub-agent text"}`))
		client.handleNotification("turn/completed", json.RawMessage(`{"threadId":"sub-thread","turn":{"id":"sub-turn","status":"completed"}}`))

		Expect(drainEvents(turn)).To(BeEmpty(), "a sub-agent's session start, text and completion are not the run's")
		Expect(turn.terminal).NotTo(BeClosed(), "a sub-agent finishing must not end the parent turn")

		client.handleNotification("item/agentMessage/delta", json.RawMessage(`{"threadId":"main-thread","itemId":"m2","delta":"parent text"}`))
		Expect(drainEvents(turn)).To(ConsistOf(HaveField("Text", "parent text")))
	})

	It("still reports the run's own thread start before its id is known", func() {
		client, turn := activeGinkgoTurn()

		client.handleNotification("thread/started", json.RawMessage(`{"thread":{"id":"main-thread"}}`))

		Expect(drainEvents(turn)).To(ConsistOf(SatisfyAll(
			HaveField("Kind", ai.EventSystem), HaveField("SessionID", "main-thread"))))
	})
})
