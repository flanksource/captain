package ai

import (
	"encoding/json"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// SchemaInstruction is the trailing prompt block that asks an agentic backend
// which cannot enforce a schema natively (e.g. cmux, or a raw CLI) to reply with
// only a JSON object conforming to schemaJSON. Providers that support structured
// output natively never need it.
func SchemaInstruction(schemaJSON string) string {
	var b strings.Builder
	b.WriteString("When you are done, respond with ONLY a single JSON object that")
	b.WriteString(" conforms to this JSON Schema — no prose, no markdown fences:\n")
	b.WriteString(schemaJSON)
	return b.String()
}

// StripMarkdownFences removes common markdown fence prefixes/suffixes
// (```json / ```yaml / ```) from a string.
func StripMarkdownFences(s string) string {
	for _, prefix := range []string{"```json\n", "```json", "```yaml\n", "```yaml", "```yml\n", "```yml", "```\n", "```"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimSuffix(s, "\n```")
	s = strings.TrimSuffix(s, "```")
	return s
}

// ExtractYAMLBlock returns the content between the first pair of --- delimiters,
// or "" when there is no such block.
func ExtractYAMLBlock(s string) string {
	parts := strings.Split(s, "---")
	if len(parts) >= 3 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

// ExtractText unwraps a JSON envelope ({"result": "..."} and friends) down to the
// text it carries; anything that is not such an envelope passes through unchanged.
func ExtractText(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return raw
	}
	var wrapper map[string]any
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return raw
	}
	for _, key := range []string{"result", "text", "response", "content"} {
		if v, ok := wrapper[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return raw
}

// ExtractJSONObject finds the first complete, valid JSON object in s using
// bracket-counting that respects JSON string escaping. ok is false when none
// closes and validates.
func ExtractJSONObject(s string) (string, bool) { return extractBalanced(s, '{', '}') }

// ExtractJSONArray finds the first complete, valid JSON array in s using
// bracket-counting that respects JSON string escaping.
func ExtractJSONArray(s string) (string, bool) { return extractBalanced(s, '[', ']') }

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
				if isValidJSONText(candidate) {
					return candidate, true
				}
			}
		}
	}
	return "", false
}

func isValidJSONText(s string) bool {
	var v any
	return json.Unmarshal([]byte(s), &v) == nil
}

// ParseStructured extracts a T from an agent's raw reply: a bare JSON/YAML body,
// a JSON envelope carrying the text (result/text/response/content keys), fenced
// JSON, a `---` YAML document, or a JSON object embedded in surrounding prose. A
// candidate only counts when validate accepts it; the last validation failure is
// surfaced so a decoded-but-wrong payload is diagnosable. It is the tolerant
// counterpart to ExecuteTyped for backends that reply with free-form text.
func ParseStructured[T any](raw string, validate func(*T) error) (*T, error) {
	p := structuredParser[T]{validate: validate}
	if v := p.try(raw); v != nil {
		return v, nil
	}

	text := ExtractText(raw)
	text = StripMarkdownFences(text)
	text = strings.TrimSpace(text)
	if v := p.try(text); v != nil {
		return v, nil
	}

	if embedded, ok := ExtractJSONObject(text); ok {
		if v := p.try(embedded); v != nil {
			return v, nil
		}
	}

	preview := raw
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}
	if p.lastValidateErr != nil {
		return nil, fmt.Errorf("reply decoded but failed validation: %w (preview: %s)", p.lastValidateErr, preview)
	}
	return nil, fmt.Errorf("failed to parse structured reply (preview: %s)", preview)
}

type structuredParser[T any] struct {
	validate        func(*T) error
	lastValidateErr error
}

// try decodes text as JSON, then YAML, then a `---` YAML document, returning the
// first candidate validate accepts.
func (p *structuredParser[T]) try(text string) *T {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if v := p.decode(text, json.Unmarshal); v != nil {
		return v
	}
	if v := p.decode(text, yamlUnmarshal); v != nil {
		return v
	}
	if block := ExtractYAMLBlock(text); block != "" {
		if v := p.decode(block, yamlUnmarshal); v != nil {
			return v
		}
	}
	return nil
}

// yamlUnmarshal adapts sigs.k8s.io/yaml's variadic signature to the plain
// unmarshal shape decode expects. It respects json struct tags.
func yamlUnmarshal(data []byte, v any) error { return yaml.Unmarshal(data, v) }

func (p *structuredParser[T]) decode(text string, unmarshal func([]byte, any) error) *T {
	var v T
	if err := unmarshal([]byte(text), &v); err != nil {
		return nil
	}
	if p.validate != nil {
		if err := p.validate(&v); err != nil {
			p.lastValidateErr = err
			return nil
		}
	}
	return &v
}
