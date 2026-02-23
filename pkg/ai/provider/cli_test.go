package provider

import "testing"

func TestMapClaudeCodeModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-code-sonnet", "claude-sonnet-4"},
		{"claude-code-opus", "claude-3-opus-20240229"},
		{"claude-code-haiku", "claude-3-5-haiku-20241022"},
		{"claude-code-sonnet-3.5", "claude-3-5-sonnet-20241022"},
		{"claude-code-claude-sonnet-4", "claude-sonnet-4"},
		{"claude-code-opus-4-6", "claude-opus-4-6"},
		{"claude-code-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-code-sonnet-4-5", "claude-sonnet-4-5"},
		{"claude-code-haiku-4-5", "claude-haiku-4-5"},
		{"claude-code-unknown", "claude-sonnet-4"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := MapClaudeCodeModel(tt.input)
			if got != tt.want {
				t.Errorf("MapClaudeCodeModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseStderr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"error line", "something\nerror: bad thing\nmore", "error: bad thing"},
		{"no errors", "line1\nline2", "line1; line2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseStderr(tt.input)
			if got != tt.want {
				t.Errorf("ParseStderr = %q, want %q", got, tt.want)
			}
		})
	}
}
