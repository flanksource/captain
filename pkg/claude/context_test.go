package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCategorizeEntries(t *testing.T) {
	entries := []HistoryEntry{
		{
			Message: Message{
				Role: MessageRoleUser,
				Content: []ContentBlock{
					{Type: ContentTypeText, Text: "Please read the file"},
				},
			},
		},
		{
			Message: Message{
				Role: MessageRoleAssistant,
				Content: []ContentBlock{
					{Type: ContentTypeThinking, Thinking: "Let me think about this carefully and read the file"},
					{Type: ContentTypeText, Text: "I'll read that file for you."},
					{Type: ContentTypeToolUse, Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/foo.go"}`)},
				},
			},
		},
		{
			Message: Message{
				Role: MessageRoleUser,
				Content: []ContentBlock{
					{Type: ContentTypeToolResult, ToolUseID: "tool_1", Content: json.RawMessage(`"package main\nfunc main() {}\n"`)},
				},
			},
		},
	}

	cb := CategorizeEntries(entries)

	assert.Greater(t, cb.Total, 0)
	assert.Greater(t, cb.Categories[CategoryUserMessage], 0)
	assert.Greater(t, cb.Categories[CategoryThinking], 0)
	assert.Greater(t, cb.Categories[CategoryAssistantText], 0)
	assert.Greater(t, cb.Categories[CategoryToolCall], 0)
	assert.Greater(t, cb.Categories[CategoryToolOutput], 0)

	// Total should equal sum of all categories
	var sum int
	for _, v := range cb.Categories {
		sum += v
	}
	assert.Equal(t, cb.Total, sum)
}

func TestCategorizeEntries_Empty(t *testing.T) {
	cb := CategorizeEntries(nil)
	assert.Equal(t, 0, cb.Total)
	assert.NotNil(t, cb.Categories)
}

func TestClassifyBlock(t *testing.T) {
	tests := []struct {
		role     MessageRole
		ct       ContentType
		expected ContentCategory
	}{
		{MessageRoleAssistant, ContentTypeToolUse, CategoryToolCall},
		{MessageRoleAssistant, ContentTypeServerToolUse, CategoryToolCall},
		{MessageRoleAssistant, ContentTypeMCPToolUse, CategoryToolCall},
		{MessageRoleUser, ContentTypeToolResult, CategoryToolOutput},
		{MessageRoleUser, ContentTypeServerToolResult, CategoryToolOutput},
		{MessageRoleUser, ContentTypeMCPToolResult, CategoryToolOutput},
		{MessageRoleAssistant, ContentTypeThinking, CategoryThinking},
		{MessageRoleAssistant, ContentTypeRedactedThinking, CategoryThinking},
		{MessageRoleUser, ContentTypeText, CategoryUserMessage},
		{MessageRoleAssistant, ContentTypeText, CategoryAssistantText},
		{"system", ContentTypeText, CategorySystemContext},
	}
	for _, tt := range tests {
		t.Run(string(tt.role)+"/"+string(tt.ct), func(t *testing.T) {
			assert.Equal(t, tt.expected, classifyBlock(tt.role, tt.ct))
		})
	}
}
