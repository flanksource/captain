package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/database"
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

func TestProjectOptionsFromAggregatesMergesClaudeCodexAndLive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeProject := filepath.Join(home, "work", "claude-project")
	codexProject := filepath.Join(home, "work", "codex-project")
	liveProject := filepath.Join(home, "work", "live-project")
	for _, dir := range []string{claudeProject, codexProject, liveProject} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		markProjectRoot(t, dir)
	}

	started := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)
	aggregates := []database.ProjectSessionAggregate{
		{Source: "claude", CWD: claudeProject, SessionCount: 1, LastActivityAt: &started},
		{Source: "codex", CWD: codexProject, SessionCount: 1, LastActivityAt: &started},
		{Source: "codex", CWD: liveProject, SessionCount: 1, LastActivityAt: &started, ProcessActive: true},
	}

	result := projectOptionsFromAggregates(aggregates)
	if result.Total != 3 {
		t.Fatalf("projects = %+v", result)
	}
	assertProjectOption(t, result.Projects, claudeProject, "claude")
	assertProjectOption(t, result.Projects, codexProject, "codex")
	assertProjectOption(t, result.Projects, liveProject, "live")
	assertProjectOption(t, result.Projects, liveProject, "codex")
}

func assertProjectOption(t *testing.T, projects []ProjectOption, path, source string) {
	t.Helper()
	for _, project := range projects {
		if project.Value != path {
			continue
		}
		if project.Path != path {
			t.Fatalf("project path = %q, want %q", project.Path, path)
		}
		if project.Label == "" {
			t.Fatalf("empty label for %+v", project)
		}
		if !slices.Contains(project.Sources, source) {
			t.Fatalf("sources for %q = %v, want %q", path, project.Sources, source)
		}
		return
	}
	t.Fatalf("project %q not found in %+v", path, projects)
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
