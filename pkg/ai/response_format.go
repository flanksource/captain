package ai

import (
	"encoding/json"
	"strings"
)

// IsMarkdown distinguishes prose and Markdown from complete or malformed JSON.
func IsMarkdown(text string) bool {
	text = strings.TrimSpace(text)
	return text != "" && !json.Valid([]byte(text)) && !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[")
}
