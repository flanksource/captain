package claudeagent

import (
	"encoding/json"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude Agent interruption", func() {
	It("does not queue a terminal result while an interrupt is in progress", func() {
		turn := &turnState{
			inbox:   make(chan ai.Event, 1),
			term:    make(chan struct{}),
			quit:    make(chan struct{}),
			pending: 1,
		}
		turn.interrupting.Store(true)
		provider := &Provider{model: testModel}
		provider.setActive(turn)

		provider.onNotification(notifyTurnDone, json.RawMessage(`{
			"success":false,
			"subtype":"error_during_execution",
			"session_id":"session-interrupted"
		}`))

		Consistently(turn.inbox, 50*time.Millisecond).ShouldNot(Receive())
		Eventually(turn.term).Should(BeClosed())
	})
})
