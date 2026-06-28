package provider

import "testing"

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
