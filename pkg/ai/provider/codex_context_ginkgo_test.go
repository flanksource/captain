package provider

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex provider context", func() {
	DescribeTable("uses the latest native snapshot independently of cumulative billing",
		func(tokenUsage string, expected *api.ContextUsage) {
			client, turn := activeGinkgoTurn()
			client.handleNotification("thread/tokenUsage/updated", json.RawMessage(`{"threadId":"thread-1","tokenUsage":`+tokenUsage+`}`))
			client.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
			events := drainEvents(turn)
			Expect(events).To(HaveLen(1))
			Expect(events[0].Context).To(Equal(expected))
		},
		Entry("the reported screenshot turn", `{"total":{"inputTokens":660747,"cachedInputTokens":582144,"outputTokens":2523},"last":{"inputTokens":89350,"cachedInputTokens":88704,"outputTokens":245,"totalTokens":89595},"modelContextWindow":258400}`, &api.ContextUsage{UsedTokens: 89595, WindowTokens: 258400, FreePercent: 69}),
		Entry("reported zero", `{"total":{"inputTokens":660747},"last":{"totalTokens":0},"modelContextWindow":258400}`, &api.ContextUsage{WindowTokens: 258400, FreePercent: 100}),
		Entry("missing latest usage", `{"total":{"inputTokens":660747},"modelContextWindow":258400}`, nil),
		Entry("missing window", `{"total":{"inputTokens":660747},"last":{"totalTokens":89595},"modelContextWindow":null}`, nil),
	)
})
