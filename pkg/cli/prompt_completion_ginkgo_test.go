package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prompt completion callbacks", func() {
	DescribeTable("reports every terminal outcome exactly once outside the stream lock",
		func(failed bool) {
			stream := newRunStream()
			stream.setRun(PromptRunFrame{RunID: "run-callback", SessionID: "shared-session", Model: "test-model"})
			var outcomes []PromptRunSummary
			stream.onComplete = func(summary PromptRunSummary) {
				stream.state()
				outcomes = append(outcomes, summary)
			}
			if failed {
				stream.fail("admission refused")
			} else {
				stream.complete(PromptRunSummary{Success: true})
			}
			stream.fail("duplicate completion")
			Expect(outcomes).To(HaveLen(1))
			Expect(outcomes[0].RunID).To(Equal("run-callback"))
			Expect(outcomes[0].SessionID).To(Equal("shared-session"))
			Expect(outcomes[0].Success).To(Equal(!failed))
			if failed {
				Expect(outcomes[0].Error).To(Equal("admission refused"))
			}
		},
		Entry("success", false),
		Entry("failure before a provider event", true),
	)
})
