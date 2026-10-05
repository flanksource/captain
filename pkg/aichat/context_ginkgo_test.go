package aichat_test

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Chat provider context metadata", func() {
	DescribeTable("streams the provider snapshot independently of billing usage",
		func(context *api.ContextUsage) {
			recorder, err := recordEvents(api.Event{Kind: api.EventResult, Success: true,
				Context: context, Usage: &api.Usage{InputTokens: 660747, CacheReadTokens: 582144}})
			Expect(err).NotTo(HaveOccurred())
			parts := decodedDataLines(recorder.Body.String())
			metadata := parts[len(parts)-1]["messageMetadata"].(map[string]any)
			Expect(metadata).NotTo(HaveKey("contextTokens"))
			if context == nil {
				Expect(metadata).NotTo(HaveKey("context"))
			} else {
				Expect(metadata).To(HaveKeyWithValue("context", map[string]any{
					"usedTokens": float64(context.UsedTokens), "windowTokens": float64(context.WindowTokens), "freePercent": float64(context.FreePercent),
				}))
			}
		},
		Entry("native context", &api.ContextUsage{UsedTokens: 89595, WindowTokens: 258400, FreePercent: 69}),
		Entry("reported zero", &api.ContextUsage{WindowTokens: 258400, FreePercent: 100}),
		Entry("missing telemetry", (*api.ContextUsage)(nil)),
	)
})
