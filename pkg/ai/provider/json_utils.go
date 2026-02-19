package provider

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	codeBlockRegex  = regexp.MustCompile("(?s)```(?:json)?\\s*(.+?)```")
	jsonObjectRegex = regexp.MustCompile(`(?s)\{.*\}`)
	jsonArrayRegex  = regexp.MustCompile(`(?s)\[.*\]`)
)

func CleanupJSONResponse(response string) string {
	response = strings.TrimSpace(response)
	if response == "" {
		return response
	}

	if isValidJSON(response) {
		return response
	}

	if matches := codeBlockRegex.FindStringSubmatch(response); len(matches) > 1 {
		if extracted := strings.TrimSpace(matches[1]); isValidJSON(extracted) {
			return extracted
		}
	}

	if match := jsonObjectRegex.FindString(response); match != "" && isValidJSON(match) {
		return match
	}

	if match := jsonArrayRegex.FindString(response); match != "" && isValidJSON(match) {
		return match
	}

	return response
}

func isValidJSON(s string) bool {
	var js any
	return json.Unmarshal([]byte(s), &js) == nil
}

func UnmarshalWithCleanup(data string, v any) error {
	if err := json.Unmarshal([]byte(data), v); err == nil {
		return nil
	}
	return json.Unmarshal([]byte(CleanupJSONResponse(data)), v)
}

func StripMarkdownFences(s string) string {
	for _, prefix := range []string{"```json\n", "```json", "```yaml\n", "```yaml", "```yml\n", "```yml", "```\n", "```"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimSuffix(s, "\n```")
	s = strings.TrimSuffix(s, "```")
	return s
}

func ExtractYAMLBlock(s string) string {
	parts := strings.Split(s, "---")
	if len(parts) >= 3 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}
