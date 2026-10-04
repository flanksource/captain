package session

import (
	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"
)

var _ = ginkgo.Describe("Estimated tool costs", func() {
	ginkgo.It("splits cumulative usage deltas across a batch without assigning the final answer to a tool", func() {
		first := api.Usage{InputTokens: 101, OutputTokens: 11, CacheReadTokens: 51, ReasoningTokens: 3}
		second := api.Usage{InputTokens: 151, OutputTokens: 21, CacheReadTokens: 71, ReasoningTokens: 5}
		uses := []history.ToolUse{
			{Tool: "Read", ToolUseID: "read", Input: map[string]any{"file_path": "example.go"}},
			{Tool: "Bash", ToolUseID: "shell", Input: map[string]any{"command": "pwd"}},
			tokenCountUse("gpt-5", first, first),
			{Tool: "Grep", ToolUseID: "search", Input: map[string]any{"pattern": "example"}},
			tokenCountUse("gpt-5", api.Usage{InputTokens: 999}, second),
			{Tool: "Assistant", Input: map[string]any{"text": "Finished"}},
			tokenCountUse("gpt-5", api.Usage{}, api.Usage{InputTokens: 181, OutputTokens: 31, CacheReadTokens: 81, ReasoningTokens: 5}),
		}
		s := buildCodexSession(uses, nil)
		Expect(s.Messages).To(HaveLen(4))
		for _, message := range s.Messages[:3] {
			Expect(message.Parts[0].EstimatedCost).NotTo(BeNil())
		}
		Expect(s.Messages[0].Parts[0].EstimatedCost.SharedCalls).To(Equal(2))
		Expect(usageFromCost(s.Messages[0].Parts[0].EstimatedCost.Cost)).To(Equal(api.Usage{
			InputTokens: 51, OutputTokens: 6, CacheReadTokens: 26, ReasoningTokens: 2,
		}))
		Expect(usageFromCost(s.Messages[1].Parts[0].EstimatedCost.Cost)).To(Equal(api.Usage{
			InputTokens: 50, OutputTokens: 5, CacheReadTokens: 25, ReasoningTokens: 1,
		}))
		Expect(usageFromCost(s.Messages[2].Parts[0].EstimatedCost.Cost)).To(Equal(api.Usage{
			InputTokens: 50, OutputTokens: 10, CacheReadTokens: 20, ReasoningTokens: 2,
		}))
		Expect(s.Messages[3].Parts[0].EstimatedCost).To(BeNil())
		Expect(s.Messages[0].Parts[0].EstimatedCost.Cost.Total()).To(BeNumerically("~", s.Messages[1].Parts[0].EstimatedCost.Cost.Total()))
		encoded, err := json.Marshal(s)
		Expect(err).NotTo(HaveOccurred())
		var decoded Session
		Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
		Expect(decoded.Messages[0].Parts[0].EstimatedCost).To(Equal(s.Messages[0].Parts[0].EstimatedCost))
	})

	ginkgo.It("leaves a call without a usage event unpriced across a new user prompt", func() {
		s := buildCodexSession([]history.ToolUse{
			{Tool: "Read", ToolUseID: "missing-usage"},
			{Tool: "User", Input: map[string]any{"text": "Another request"}},
			{Tool: "Bash", ToolUseID: "new-call"},
			tokenCountUse("gpt-5", api.Usage{InputTokens: 40}, api.Usage{InputTokens: 40}),
		}, nil)
		Expect(s.Messages[0].Parts[0].EstimatedCost).To(BeNil())
		Expect(s.Messages[2].Parts[0].EstimatedCost).NotTo(BeNil())
		Expect(s.Messages[2].Parts[0].EstimatedCost.SharedCalls).To(Equal(1))
	})

	ginkgo.It("reoffers incremental tool messages when their usage arrives in a later batch", func() {
		accumulator := NewCodexAccumulator("session.jsonl")
		accumulator.Add(nil, []history.ToolUse{{Tool: "Bash", ToolUseID: "incremental-call"}})
		first := accumulator.Project(nil)
		Expect(first.Messages).To(HaveLen(1))
		Expect(first.Messages[0].Parts[0].EstimatedCost).To(BeNil())

		accumulator.Add(nil, []history.ToolUse{tokenCountUse("gpt-5", api.Usage{InputTokens: 40}, api.Usage{InputTokens: 40})})
		priced := accumulator.Project(nil)
		Expect(priced.Messages).To(HaveLen(1))
		Expect(priced.Messages[0].ID).To(Equal("incremental-call"))
		Expect(priced.Messages[0].Parts[0].EstimatedCost).NotTo(BeNil())
		Expect(priced.Messages[0].Parts[0].EstimatedCost.Cost.InputTokens).To(Equal(40))
		Expect(accumulator.Project(nil).Messages).To(BeEmpty())
	})

})
