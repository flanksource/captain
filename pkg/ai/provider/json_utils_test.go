package provider

import (
	"testing"
)

func TestCleanupJSONResponse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"valid json", `{"key": "value"}`, `{"key": "value"}`},
		{"markdown code block", "```json\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"markdown code block no lang", "```\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"embedded json", `Here is the result: {"key": "value"} done`, `{"key": "value"}`},
		{"json array", `Text before [1,2,3] after`, `[1,2,3]`},
		{"empty", "", ""},
		{"no json", "just plain text", "just plain text"},
		{
			"ansi codes around json",
			"\x1b[32m{\"ok\": true}\x1b[0m",
			`{"ok": true}`,
		},
		{
			"prose then code block",
			"Here is the data:\n\n```json\n{\"name\": \"test\", \"count\": 42}\n```\n\nHope that helps!",
			`{"name": "test", "count": 42}`,
		},
		{
			"nested markdown code block",
			"Some explanation\n\n```json\n{\"items\": [{\"id\": 1}, {\"id\": 2}]}\n```",
			`{"items": [{"id": 1}, {"id": 2}]}`,
		},
		{
			"multiline json in code block",
			"```json\n{\n  \"a\": 1,\n  \"b\": 2\n}\n```",
			"{\n  \"a\": 1,\n  \"b\": 2\n}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanupJSONResponse(tt.input)
			if got != tt.want {
				t.Errorf("CleanupJSONResponse() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractJSONLines(t *testing.T) {
	input := `{"type":"thread.started","id":"abc"}
2026-01-01T00:00:00Z ERROR some log line
{"type":"turn.started"}
not json
{"type":"item.completed","item":{"text":"hello"}}
{"type":"turn.completed","usage":{"input_tokens":100}}
`
	lines := ExtractJSONLines(input)
	if len(lines) != 4 {
		t.Fatalf("expected 4 JSON lines, got %d", len(lines))
	}

	// Verify first and last
	if string(lines[0]) != `{"type":"thread.started","id":"abc"}` {
		t.Errorf("first line = %s", lines[0])
	}
	if string(lines[3]) != `{"type":"turn.completed","usage":{"input_tokens":100}}` {
		t.Errorf("last line = %s", lines[3])
	}
}

func TestExtractJSONLinesWithANSI(t *testing.T) {
	input := "\x1b[33m{\"type\":\"started\"}\x1b[0m\ngarbage\n{\"type\":\"done\"}"
	lines := ExtractJSONLines(input)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSON lines, got %d", len(lines))
	}
}

func TestFindJSONLine(t *testing.T) {
	input := `{"type":"start"}
{"type":"message","text":"hello"}
{"type":"done"}`

	raw, ok := FindJSONLine(input, func(obj map[string]any) bool {
		return obj["type"] == "message"
	})
	if !ok {
		t.Fatal("expected to find message line")
	}
	if string(raw) != `{"type":"message","text":"hello"}` {
		t.Errorf("got %s", raw)
	}
}

func TestFindLastJSONLine(t *testing.T) {
	input := `{"type":"item","n":1}
{"type":"item","n":2}
{"type":"item","n":3}
{"type":"done"}`

	raw, ok := FindLastJSONLine(input, func(obj map[string]any) bool {
		return obj["type"] == "item"
	})
	if !ok {
		t.Fatal("expected to find item line")
	}
	if string(raw) != `{"type":"item","n":3}` {
		t.Errorf("got %s", raw)
	}
}

func TestExtractJSONObject(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{"simple", `prefix {"a":1} suffix`, `{"a":1}`, true},
		{"nested", `x {"a":{"b":2}} y`, `{"a":{"b":2}}`, true},
		{"no object", "just text", "", false},
		{"with ansi", "\x1b[31m{\"x\":1}\x1b[0m", `{"x":1}`, true},
		{"string with braces", `before {"k": "val{ue}"} after`, `{"k": "val{ue}"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractJSONObject(tt.input)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripMarkdownFences(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"json fence", "```json\n{\"key\": \"val\"}\n```", `{"key": "val"}`},
		{"plain fence", "```\ncode\n```", "code"},
		{"yaml fence", "```yaml\nkey: val\n```", "key: val"},
		{"no fence", "plain text", "plain text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripMarkdownFences(tt.input)
			if got != tt.want {
				t.Errorf("StripMarkdownFences(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUnmarshalWithCleanup(t *testing.T) {
	var result map[string]string

	if err := UnmarshalWithCleanup(`{"name": "test"}`, &result); err != nil {
		t.Fatalf("direct JSON: %v", err)
	}
	if result["name"] != "test" {
		t.Errorf("got %q, want %q", result["name"], "test")
	}

	result = nil
	if err := UnmarshalWithCleanup("```json\n{\"name\": \"test2\"}\n```", &result); err != nil {
		t.Fatalf("markdown JSON: %v", err)
	}
	if result["name"] != "test2" {
		t.Errorf("got %q, want %q", result["name"], "test2")
	}
}

func TestExtractMarkdownCodeBlocks(t *testing.T) {
	input := "# Title\n\nSome text\n\n```go\nfunc main() {}\n```\n\n```json\n{\"x\": 1}\n```\n\nMore text\n\n```\nplain block\n```"

	// All blocks
	all := ExtractMarkdownCodeBlocks(input)
	if len(all) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(all))
	}

	// Filter by language
	goBlocks := ExtractMarkdownCodeBlocks(input, "go")
	if len(goBlocks) != 1 {
		t.Fatalf("expected 1 go block, got %d", len(goBlocks))
	}
	if goBlocks[0] != "func main() {}\n" {
		t.Errorf("go block = %q", goBlocks[0])
	}

	jsonBlocks := ExtractMarkdownCodeBlocks(input, "json")
	if len(jsonBlocks) != 1 {
		t.Fatalf("expected 1 json block, got %d", len(jsonBlocks))
	}
}

func TestIsMarkdown(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain text", "hello world", false},
		{"heading", "# Title\n\nSome text", true},
		{"code block", "```\ncode\n```", true},
		{"bold", "This is **bold** text", true},
		{"link", "Check [this](http://example.com)", true},
		{"inline code", "Use `foo` here", true},
		{"json", `{"key": "value"}`, false},
		{"empty", "", false},
		{"just backticks no pair", "one ` backtick", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsMarkdown(tt.input)
			if got != tt.want {
				t.Errorf("IsMarkdown(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
