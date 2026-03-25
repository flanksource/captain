package claude

// ContentCategory classifies where context tokens are being spent.
type ContentCategory string

const (
	CategoryToolOutput    ContentCategory = "tool-output"
	CategoryToolCall      ContentCategory = "tool-call"
	CategoryUserMessage   ContentCategory = "user-message"
	CategoryAssistantText ContentCategory = "assistant-text"
	CategoryThinking      ContentCategory = "thinking"
	CategorySystemContext  ContentCategory = "system-context"
)

// ContextBreakdown tracks estimated token consumption by content category.
type ContextBreakdown struct {
	Categories map[ContentCategory]int `json:"categories"`
	Total      int                     `json:"total"`
}

// CategorizeEntries estimates token usage by content category across history entries.
func CategorizeEntries(entries []HistoryEntry) ContextBreakdown {
	cb := ContextBreakdown{Categories: make(map[ContentCategory]int)}

	for _, entry := range entries {
		role := entry.Message.Role
		for _, block := range entry.Message.Content {
			tokens := estimateBlockTokens(block)
			if tokens == 0 {
				continue
			}

			cat := classifyBlock(role, block.Type)
			cb.Categories[cat] += tokens
			cb.Total += tokens
		}
	}
	return cb
}

func classifyBlock(role MessageRole, ct ContentType) ContentCategory {
	switch {
	case ct == ContentTypeToolUse || ct == ContentTypeServerToolUse || ct == ContentTypeMCPToolUse:
		return CategoryToolCall
	case ct == ContentTypeToolResult || ct == ContentTypeServerToolResult || ct == ContentTypeMCPToolResult:
		return CategoryToolOutput
	case ct == ContentTypeThinking || ct == ContentTypeRedactedThinking:
		return CategoryThinking
	case role == MessageRoleUser:
		return CategoryUserMessage
	case role == MessageRoleAssistant:
		return CategoryAssistantText
	default:
		return CategorySystemContext
	}
}

func estimateBlockTokens(block ContentBlock) int {
	switch block.Type {
	case ContentTypeText:
		return EstimateTokens(block.Text)
	case ContentTypeThinking:
		return EstimateTokens(block.Thinking)
	case ContentTypeToolUse, ContentTypeServerToolUse, ContentTypeMCPToolUse:
		return EstimateContentTokens(block.Input)
	case ContentTypeToolResult, ContentTypeServerToolResult, ContentTypeMCPToolResult:
		return EstimateContentTokens(block.Content)
	case ContentTypeRedactedThinking:
		return 0
	case ContentTypeImage:
		return 1000 // rough estimate for image tokens
	default:
		return 0
	}
}
