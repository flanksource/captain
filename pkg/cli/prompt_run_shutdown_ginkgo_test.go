package cli

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("stopping the prompt runs a host drives on shutdown", func() {
	const settleTimeout = 200 * time.Millisecond

	var broker *runBroker

	BeforeEach(func() { broker = &runBroker{runs: map[string]*runStream{}} })

	// settlesOnStop is a run whose stop makes it settle, as a run does once its
	// recorder files the cancellation.
	settlesOnStop := func(runID string) *runStream {
		stream := broker.create(runID)
		stream.setCancel(func() { go stream.fail("stopped") })
		return stream
	}

	It("stops every live run and waits for each to settle", func() {
		first, second := settlesOnStop("run-shutdown-1"), settlesOnStop("run-shutdown-2")

		Expect(broker.stop(settleTimeout)).To(BeZero())

		Expect(first.wasStopped()).To(BeTrue())
		Expect(second.wasStopped()).To(BeTrue())
		Expect(first.finished).To(BeClosed())
		Expect(second.finished).To(BeClosed())
	})

	It("leaves a finished run alone", func() {
		finished := broker.create("run-shutdown-done")
		finished.complete(PromptRunSummary{Success: true})

		Expect(broker.stop(settleTimeout)).To(BeZero())

		Expect(finished.wasStopped()).To(BeFalse())
	})

	It("gives up at the timeout and reports the runs still unsettled", func() {
		settlesOnStop("run-shutdown-settles")
		broker.create("run-shutdown-stuck").setCancel(func() {})

		started := time.Now()
		Expect(broker.stop(settleTimeout)).To(Equal(1))
		Expect(time.Since(started)).To(BeNumerically("<", 5*settleTimeout))
	})
})
