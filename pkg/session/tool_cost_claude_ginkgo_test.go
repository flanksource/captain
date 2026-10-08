package session

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Estimated tool costs for Claude", func() {
	ginkgo.It("shares one response across its tool blocks without counting repeated usage or prose", func() {
		usage := &claude.Usage{InputTokens: 101, OutputTokens: 11, CacheReadInputTokens: 51, CacheCreationInputTokens: 21}
		entries := []claude.HistoryEntry{
			assistantEntry("thinking", "", "claude-sonnet-4-5", usage, claude.ContentBlock{Type: claude.ContentTypeThinking, Thinking: "Checking"}),
			assistantEntry("read", "", "claude-sonnet-4-5", usage, toolUseBlock("read-call", "Read", nil)),
			assistantEntry("shell", "", "claude-sonnet-4-5", nil, toolUseBlock("shell-call", "Bash", nil)),
			assistantEntry("answer", "", "claude-sonnet-4-5", usage, claude.ContentBlock{Type: claude.ContentTypeText, Text: "Done"}),
		}
		entries[0].Message.ID = "request-tools"
		entries[1].Message.ID = "request-tools"
		entries[2].Message.ID = "request-tools"
		entries[3].Message.ID = "request-answer"
		s := buildSession(claude.ParsedSession{SessionID: "root", Transcripts: []claude.ParsedTranscript{{Entries: entries}}})
		Expect(s.Messages[0].Parts[0].EstimatedCost).To(BeNil())
		Expect(s.Messages[3].Parts[0].EstimatedCost).To(BeNil())
		for _, message := range s.Messages[1:3] {
			Expect(message.Parts[0].EstimatedCost).NotTo(BeNil())
			Expect(message.Parts[0].EstimatedCost.SharedCalls).To(Equal(2))
		}
		Expect(usageFromCost(s.Messages[1].Parts[0].EstimatedCost.Cost)).To(Equal(api.Usage{
			InputTokens: 51, OutputTokens: 6, CacheReadTokens: 26, CacheWriteTokens: 11,
		}))
		Expect(usageFromCost(s.Messages[2].Parts[0].EstimatedCost.Cost)).To(Equal(api.Usage{
			InputTokens: 50, OutputTokens: 5, CacheReadTokens: 25, CacheWriteTokens: 10,
		}))
		Expect(s.Messages[1].Parts[0].EstimatedCost.Cost.Total()).To(BeNumerically("~", 0.000281025, 1e-10))
		Expect(s.Cost.Total()).To(BeNumerically("~", 0.0011241, 1e-10))
		projected, _ := s.ToUIMessages()
		Expect(projected[1].Parts[0].EstimatedCost).To(Equal(s.Messages[1].Parts[0].EstimatedCost))
	})

	ginkgo.It("keeps response identity local to each agent and leaves missing usage unavailable", func() {
		root := assistantEntry("root-call", "", "claude-sonnet-4-5", &claude.Usage{InputTokens: 40}, toolUseBlock("root-tool", "Read", nil))
		child := assistantEntry("child-call", "", "claude-sonnet-4-5", &claude.Usage{InputTokens: 80}, toolUseBlock("child-tool", "Read", nil))
		missing := assistantEntry("missing", "", "claude-sonnet-4-5", nil, toolUseBlock("missing-tool", "Bash", nil))
		root.Message.ID = "same-response-id"
		child.Message.ID = "same-response-id"
		s := buildSession(claude.ParsedSession{SessionID: "root", Transcripts: []claude.ParsedTranscript{
			{Entries: []claude.HistoryEntry{root, missing}},
			{IsAgent: true, AgentID: "child", Entries: []claude.HistoryEntry{child}},
		}})
		Expect(s.Messages[0].Parts[0].EstimatedCost).NotTo(BeNil())
		Expect(s.Messages[0].Parts[0].EstimatedCost.Cost.InputTokens).To(Equal(40))
		Expect(s.Messages[1].Parts[0].EstimatedCost).To(BeNil())
		Expect(s.Messages[2].Parts[0].EstimatedCost).NotTo(BeNil())
		Expect(s.Messages[2].Parts[0].EstimatedCost.Cost.InputTokens).To(Equal(80))
	})

	ginkgo.It("preserves every canonical token and cost bucket independently of the provider", func() {
		cost := api.Cost{Model: "example-model", InputTokens: 101, OutputTokens: 11, ReasoningTokens: 3,
			CacheReadTokens: 51, CacheWriteTokens: 21, InputCost: 0.1, OutputCost: 0.2,
			ReasoningCost: 0.03, CacheReadCost: 0.04, CacheWriteCost: 0.05, ProviderCostUSD: 0.7}
		messages := []Message{{Parts: []Part{{Type: PartTool, ToolName: "Read"}, {Type: PartTool, ToolName: "Bash"}}}}
		assignToolCosts(toolCallParts(messages), cost)
		Expect(messages[0].Parts[0].EstimatedCost).NotTo(BeNil())
		Expect(messages[0].Parts[1].EstimatedCost).NotTo(BeNil())
		first, second := messages[0].Parts[0].EstimatedCost, messages[0].Parts[1].EstimatedCost
		Expect(first.SharedCalls).To(Equal(2))
		Expect(first.Cost.Add(second.Cost)).To(Equal(api.Cost{
			Model: "example-model", InputTokens: 101, OutputTokens: 11, ReasoningTokens: 3, TotalTokens: 187,
			CacheReadTokens: 51, CacheWriteTokens: 21, InputCost: 0.1, OutputCost: 0.2,
			ReasoningCost: 0.03, CacheReadCost: 0.04, CacheWriteCost: 0.05, ProviderCostUSD: 0.7,
		}))
	})

	ginkgo.It("prices native tool calls without allocating usage to a narrative plan or tool result", func() {
		entry := assistantEntry("native-call", "", "claude-sonnet-4-5", &claude.Usage{InputTokens: 40, OutputTokens: 10},
			claude.ContentBlock{Type: claude.ContentTypeText, Text: "<proposed_plan>Inspect the file</proposed_plan>"},
			toolUseBlock("read-call", "Read", nil))
		result := claude.HistoryEntry{UUID: "result", Message: claude.Message{Role: claude.MessageRoleUser,
			Content: []claude.ContentBlock{{Type: claude.ContentTypeToolResult, ToolUseID: "read-call"}}}}
		s := buildSession(claude.ParsedSession{SessionID: "root", Transcripts: []claude.ParsedTranscript{{Entries: []claude.HistoryEntry{entry, result}}}})
		Expect(s.Messages[0].Parts).To(HaveLen(2))
		Expect(s.Messages[0].Parts[0].ToolName).To(Equal("Plan"))
		Expect(s.Messages[0].Parts[0].EstimatedCost).To(BeNil())
		Expect(s.Messages[0].Parts[1].EstimatedCost).NotTo(BeNil())
		Expect(s.Messages[0].Parts[1].EstimatedCost.SharedCalls).To(Equal(1))
		Expect(s.Messages[0].Parts[1].EstimatedCost.Cost.InputTokens).To(Equal(40))
		Expect(s.Messages[1].Parts[0].EstimatedCost).To(BeNil())
	})
})
