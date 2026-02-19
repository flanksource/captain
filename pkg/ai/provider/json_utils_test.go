package provider

import "testing"

func TestCleanupJSONResponse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"valid json", `{"key": "value"}`, `{"key": "value"}`},
		{"markdown code block", "```json\n{\"key\": \"value\"}\n```", `{"key": "value"}`},
		{"embedded json", `Here is the result: {"key": "value"} done`, `{"key": "value"}`},
		{"json array", `Text before [1,2,3] after`, `[1,2,3]`},
		{"empty", "", ""},
		{"no json", "just plain text", "just plain text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanupJSONResponse(tt.input)
			if got != tt.want {
				t.Errorf("CleanupJSONResponse(%q) = %q, want %q", tt.input, got, tt.want)
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
