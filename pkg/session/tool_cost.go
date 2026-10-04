package session

import "github.com/flanksource/captain/pkg/api"

// ToolCostEstimate allocates a model request's usage and cost equally across
// its tool calls. It includes the request's conversation context, not just the
// tool result, whose consumption belongs to a later model request.
type ToolCostEstimate struct {
	Cost        api.Cost `json:"cost"`
	SharedCalls int      `json:"sharedCalls"`
}

func assignToolCosts(parts []*Part, cost api.Cost) {
	for i, part := range parts {
		part.EstimatedCost = &ToolCostEstimate{Cost: toolCostShare(cost, len(parts), i), SharedCalls: len(parts)}
	}
}

func toolCallParts(messages []Message) []*Part {
	var parts []*Part
	for _, message := range messages {
		for i := range message.Parts {
			if isToolCall(message.Parts[i]) && !IsSyntheticEventTool(message.Parts[i].ToolName) {
				parts = append(parts, &message.Parts[i])
			}
		}
	}
	return parts
}

func toolCostShare(cost api.Cost, calls, index int) api.Cost {
	share := cost
	share.InputTokens = tokenShare(cost.InputTokens, calls, index)
	share.OutputTokens = tokenShare(cost.OutputTokens, calls, index)
	share.ReasoningTokens = tokenShare(cost.ReasoningTokens, calls, index)
	share.CacheReadTokens = tokenShare(cost.CacheReadTokens, calls, index)
	share.CacheWriteTokens = tokenShare(cost.CacheWriteTokens, calls, index)
	share.TotalTokens = usageFromCost(share).TotalTokens()
	share.InputCost /= float64(calls)
	share.OutputCost /= float64(calls)
	share.ReasoningCost /= float64(calls)
	share.CacheReadCost /= float64(calls)
	share.CacheWriteCost /= float64(calls)
	share.ProviderCostUSD /= float64(calls)
	return share
}

func tokenShare(tokens, calls, index int) int {
	share := tokens / calls
	if index < tokens%calls {
		share++
	}
	return share
}
