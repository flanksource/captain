package session

import "github.com/flanksource/captain/pkg/api"

func (a *CodexAccumulator) observeToolMessage(message Message) {
	if message.Role == "user" {
		a.pendingTools = nil
	}
	for _, part := range message.Parts {
		if isToolCall(part) && !IsSyntheticEventTool(part.ToolName) {
			a.pendingTools = append(a.pendingTools, message)
			return
		}
	}
}

func (a *CodexAccumulator) estimateToolCosts(cost api.Cost) {
	assignToolCosts(toolCallParts(a.pendingTools), cost)
	if !a.collect {
		fresh := make(map[string]bool, len(a.freshMessages))
		for _, message := range a.freshMessages {
			fresh[message.ID] = true
		}
		for _, message := range a.pendingTools {
			if !fresh[message.ID] {
				a.freshMessages = append(a.freshMessages, message)
			}
		}
	}
	a.pendingTools = nil
}
