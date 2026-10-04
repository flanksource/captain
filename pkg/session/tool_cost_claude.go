package session

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
)

type claudeToolCosts struct {
	byResponse map[string]*claudeToolResponse
	responses  []*claudeToolResponse
}

type claudeToolResponse struct {
	parts    []*Part
	cost     api.Cost
	hasUsage bool
}

func (c *claudeToolCosts) add(entry claude.HistoryEntry, message Message) {
	if !entry.IsAssistantMessage() {
		return
	}
	if c.byResponse == nil {
		c.byResponse = map[string]*claudeToolResponse{}
	}
	response := c.byResponse[entry.Message.ID]
	if response == nil || entry.Message.ID == "" {
		response = &claudeToolResponse{}
		c.responses = append(c.responses, response)
		if entry.Message.ID != "" {
			c.byResponse[entry.Message.ID] = response
		}
	}
	if !response.hasUsage && entry.Message.Usage != nil {
		response.cost = CostFromUsage(entry.Message.Usage, entry.Message.Model)
		response.hasUsage = true
	}
	for _, block := range entry.Message.Content {
		if block.Type != claude.ContentTypeToolUse || IsSyntheticEventTool(block.Name) {
			continue
		}
		for i := range message.Parts {
			if isToolCall(message.Parts[i]) && message.Parts[i].ToolCallID == block.ID {
				response.parts = append(response.parts, &message.Parts[i])
			}
		}
	}
}

func (c *claudeToolCosts) estimate() {
	for _, response := range c.responses {
		if response.hasUsage && response.cost.TotalTokens != 0 {
			assignToolCosts(response.parts, response.cost)
		}
	}
}
