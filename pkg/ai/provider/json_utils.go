package provider

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// StripANSI removes ANSI escape codes from a string.
func StripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// ---------------------------------------------------------------------------
// Markdown-aware extraction using goldmark
// ---------------------------------------------------------------------------

// markdownCodeBlocks parses markdown with goldmark and returns the content of
// all fenced code blocks whose info string matches any of the given languages
// (case-insensitive). An empty langs slice matches all code blocks.
func markdownCodeBlocks(source string, langs ...string) []string {
	src := []byte(source)
	md := goldmark.New()
	reader := text.NewReader(src)
	doc := md.Parser().Parse(reader)

	wantLang := make(map[string]bool, len(langs))
	for _, l := range langs {
		wantLang[strings.ToLower(l)] = true
	}

	var blocks []string
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		fcb, ok := n.(*ast.FencedCodeBlock)
		if !ok {
			return ast.WalkContinue, nil
		}

		// Check language filter
		if len(wantLang) > 0 {
			info := string(fcb.Language(src))
			if !wantLang[strings.ToLower(strings.TrimSpace(info))] {
				return ast.WalkContinue, nil
			}
		}

		// Collect lines of the code block
		var buf bytes.Buffer
		for i := 0; i < fcb.Lines().Len(); i++ {
			line := fcb.Lines().At(i)
			buf.Write(line.Value(src))
		}
		blocks = append(blocks, buf.String())
		return ast.WalkContinue, nil
	})
	return blocks
}

// extractMarkdownJSON uses the goldmark parser to find JSON inside markdown
// fenced code blocks (```json or untagged ```).
func extractMarkdownJSON(source string) string {
	// Try json-tagged blocks first, then any block
	for _, langs := range [][]string{{"json"}, {}} {
		for _, block := range markdownCodeBlocks(source, langs...) {
			trimmed := strings.TrimSpace(block)
			if isValidJSON(trimmed) {
				return trimmed
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Core JSON cleanup / extraction
// ---------------------------------------------------------------------------

// CleanupJSONResponse attempts to extract valid JSON from a string that may
// contain surrounding text, markdown fences, ANSI codes, or other noise.
//
// Strategies tried in order:
//  1. Raw input is already valid JSON
//  2. Strip ANSI codes, retry
//  3. Parse markdown with goldmark, extract from fenced code blocks
//  4. Bracket-counting extraction of first JSON object
//  5. Bracket-counting extraction of first JSON array
//  6. Return original input
func CleanupJSONResponse(response string) string {
	response = strings.TrimSpace(response)
	if response == "" {
		return response
	}

	// 1. Already valid
	if isValidJSON(response) {
		return response
	}

	// 2. Strip ANSI
	cleaned := strings.TrimSpace(StripANSI(response))
	if cleaned != response && isValidJSON(cleaned) {
		return cleaned
	}

	// 3. Markdown code block extraction (goldmark)
	if extracted := extractMarkdownJSON(cleaned); extracted != "" {
		return extracted
	}

	// 4. Bracket-counting object extraction
	if obj, ok := ExtractJSONObject(cleaned); ok {
		return obj
	}

	// 5. Bracket-counting array extraction
	if arr, ok := extractJSONArray(cleaned); ok {
		return arr
	}

	return response
}

// ---------------------------------------------------------------------------
// JSONL helpers
// ---------------------------------------------------------------------------

// ExtractJSONLines parses JSONL output (one JSON object per line), skipping
// non-JSON lines (e.g. log messages, ANSI output, progress indicators).
func ExtractJSONLines(output string) []json.RawMessage {
	var results []json.RawMessage
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(StripANSI(line))
		if line == "" || (line[0] != '{' && line[0] != '[') {
			continue
		}
		if isValidJSON(line) {
			results = append(results, json.RawMessage(line))
		}
	}
	return results
}

// FindJSONLine scans JSONL output for the first line where the predicate
// returns true.
func FindJSONLine(output string, predicate func(map[string]any) bool) (json.RawMessage, bool) {
	for _, raw := range ExtractJSONLines(output) {
		var obj map[string]any
		if json.Unmarshal(raw, &obj) == nil && predicate(obj) {
			return raw, true
		}
	}
	return nil, false
}

// FindLastJSONLine scans JSONL output for the last line where the predicate
// returns true.
func FindLastJSONLine(output string, predicate func(map[string]any) bool) (json.RawMessage, bool) {
	lines := ExtractJSONLines(output)
	for i := len(lines) - 1; i >= 0; i-- {
		var obj map[string]any
		if json.Unmarshal(lines[i], &obj) == nil && predicate(obj) {
			return lines[i], true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Bracket-counting extractors
// ---------------------------------------------------------------------------

// ExtractJSONObject finds and extracts the first complete JSON object from
// a string using bracket-counting to handle nested braces correctly.
func ExtractJSONObject(s string) (string, bool) {
	return extractBalanced(StripANSI(s), '{', '}')
}

// extractJSONArray finds and extracts the first complete JSON array from
// a string using bracket-counting.
func extractJSONArray(s string) (string, bool) {
	return extractBalanced(StripANSI(s), '[', ']')
}

// extractBalanced finds text from the first occurrence of open to the matching
// close character, respecting nesting and JSON string escaping.
func extractBalanced(s string, open, close byte) (string, bool) {
	start := strings.IndexByte(s, open)
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				candidate := s[start : i+1]
				if isValidJSON(candidate) {
					return candidate, true
				}
			}
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Utility helpers
// ---------------------------------------------------------------------------

func isValidJSON(s string) bool {
	var js any
	return json.Unmarshal([]byte(s), &js) == nil
}

// UnmarshalWithCleanup tries direct unmarshal first, then falls back to
// CleanupJSONResponse before retrying.
func UnmarshalWithCleanup(data string, v any) error {
	if err := json.Unmarshal([]byte(data), v); err == nil {
		return nil
	}
	return json.Unmarshal([]byte(CleanupJSONResponse(data)), v)
}

// StripMarkdownFences removes common markdown fence prefixes/suffixes.
// Prefer extractMarkdownJSON for structured extraction; this is a fast
// best-effort helper for simple cases.
func StripMarkdownFences(s string) string {
	for _, prefix := range []string{"```json\n", "```json", "```yaml\n", "```yaml", "```yml\n", "```yml", "```\n", "```"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimSuffix(s, "\n```")
	s = strings.TrimSuffix(s, "```")
	return s
}

// ExtractYAMLBlock extracts content between the first pair of --- delimiters.
func ExtractYAMLBlock(s string) string {
	parts := strings.Split(s, "---")
	if len(parts) >= 3 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

// ExtractMarkdownCodeBlocks exposes the goldmark-based code block extractor
// for use outside this package. langs filters by info string (empty = all).
func ExtractMarkdownCodeBlocks(source string, langs ...string) []string {
	return markdownCodeBlocks(source, langs...)
}

// IsMarkdown returns true if the string looks like it contains markdown
// formatting (headings, code blocks, bold, links, lists, etc.).
func IsMarkdown(s string) bool {
	if s == "" {
		return false
	}
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Fenced code block
		if strings.HasPrefix(trimmed, "```") {
			return true
		}
		// ATX headings
		if len(trimmed) > 1 && trimmed[0] == '#' && (trimmed[1] == ' ' || trimmed[1] == '#') {
			return true
		}
		// Unordered list
		if (strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ")) && len(trimmed) > 2 {
			prefix := trimmed[:2]
			if strings.HasPrefix(s, prefix) || strings.Contains(s, "\n"+prefix) {
				continue // check more lines
			}
		}
	}
	// Inline patterns: bold, links, inline code (need at least 2 occurrences for bold/code)
	if strings.Count(s, "**") >= 2 || strings.Count(s, "__") >= 2 {
		return true
	}
	if strings.Count(s, "`") >= 2 && !strings.Contains(s, "```") {
		return true
	}
	if strings.Contains(s, "](") && strings.Contains(s, "[") {
		return true
	}
	return false
}
