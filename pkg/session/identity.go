package session

import (
	"strings"
	"unicode"

	"github.com/flanksource/captain/pkg/claude"
)

const derivedSessionTitleMaxRunes = 96

// applySessionIdentity fills the user-facing session identity from canonical
// messages after source-specific metadata (for example Claude's title/slug)
// has been applied.
func applySessionIdentity(s *Session) {
	if s == nil {
		return
	}
	if s.InitialPrompt == "" {
		s.InitialPrompt = firstUserPrompt(s.Messages)
	}
	if s.Title == "" {
		s.Title = deriveSessionTitle(s.InitialPrompt)
	}
}

func firstClaudeUserPrompt(entries []claude.HistoryEntry) string {
	for _, entry := range entries {
		if !entry.IsUserMessage() {
			continue
		}
		for _, block := range entry.Message.Content {
			if block.Type != claude.ContentTypeText {
				continue
			}
			if text := strings.TrimSpace(block.Text); text != "" {
				return text
			}
		}
	}
	return ""
}

func latestClaudeSessionTitle(uses []claude.ToolUse) string {
	var title string
	for _, use := range uses {
		if use.Tool != "SessionTitle" {
			continue
		}
		if value, _ := use.Input["aiTitle"].(string); strings.TrimSpace(value) != "" {
			title = strings.TrimSpace(value)
		}
	}
	return title
}

func firstUserPrompt(messages []Message) string {
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		for _, part := range message.Parts {
			if part.Type != PartText {
				continue
			}
			if text := strings.TrimSpace(part.Text); text != "" {
				return text
			}
		}
	}
	return ""
}

func deriveSessionTitle(prompt string) string {
	collapsed := strings.Join(strings.Fields(prompt), " ")
	if collapsed == "" {
		return ""
	}
	runes := []rune(collapsed)
	if len(runes) <= derivedSessionTitleMaxRunes {
		return collapsed
	}

	cut := derivedSessionTitleMaxRunes
	for i := derivedSessionTitleMaxRunes - 1; i >= derivedSessionTitleMaxRunes/2; i-- {
		if unicode.IsSpace(runes[i]) {
			cut = i
			break
		}
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}
