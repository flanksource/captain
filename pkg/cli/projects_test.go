package cli

import (
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1048576, "1.0MB"},
		{52428800, "50.0MB"},
		{1073741824, "1.0GB"},
		{1610612736, "1.5GB"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.input)
		if got != tt.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestUuidStem(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/path/to/abc-123.jsonl", "abc-123"},
		{"/path/to/.jsonl", ""},
		{"/path/to/..jsonl", ""},
		{"/path/to/normal-uuid.jsonl", "normal-uuid"},
	}
	for _, tt := range tests {
		got := uuidStem(tt.input)
		if got != tt.expected {
			t.Errorf("uuidStem(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/simple/path", "'/simple/path'"},
		{"/path with spaces", "'/path with spaces'"},
		{"/path'quote", "'/path'\\''quote'"},
		{"/path;inject", "'/path;inject'"},
		{"/path$(cmd)", "'/path$(cmd)'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.expected {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestParseSince(t *testing.T) {
	tests := []struct {
		input     string
		wantErr   bool
		checkDays int // approximate days in the past
	}{
		{"30d", false, 30},
		{"7d", false, 7},
		{"1h", false, 0},
		{"now-30d", false, 30},
		{"now-7d", false, 7},
		{"garbage!!", true, 0},
	}

	for _, tt := range tests {
		got, err := parseSince(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseSince(%q) expected error, got %v", tt.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSince(%q) unexpected error: %v", tt.input, err)
			continue
		}
		daysDiff := int(time.Since(got).Hours() / 24)
		if tt.checkDays > 0 && (daysDiff < tt.checkDays-1 || daysDiff > tt.checkDays+1) {
			t.Errorf("parseSince(%q) = %v, expected ~%d days ago, got %d days ago", tt.input, got, tt.checkDays, daysDiff)
		}
	}
}
