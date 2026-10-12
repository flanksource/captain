package cli

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// prunerSettleWindow is how long a running pruner must stay up while its host
// context is live, so a pruner that exits early fails the spec.
const prunerSettleWindow = 100 * time.Millisecond

var _ = Describe("session host", func() {
	var (
		runs  *runBroker
		chats *chatBroker
	)

	BeforeEach(func() {
		runs = &runBroker{runs: map[string]*runStream{}}
		chats = newChatBroker()
	})

	It("stops the run pruner when the host context ends", func(ctx SpecContext) {
		hostCtx, cancel := context.WithCancel(ctx)
		host := startSessionHost(hostCtx, runs, chats)
		Consistently(host.pruned).WithTimeout(prunerSettleWindow).ShouldNot(BeClosed())

		cancel()

		Eventually(host.pruned).Should(BeClosed())
	})

	It("stops every live chat run and the pruner on stop", func(ctx SpecContext) {
		stream := newRunStream()
		chats.register(newChatSession("live-chat", claudeAgentRender(""), 0, stream, nil))
		host := startSessionHost(ctx, runs, chats)

		host.stop()

		Expect(host.pruned).To(BeClosed())
		Expect(stream.wasStopped()).To(BeTrue())
	})
})
