package claude

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFilterToolUses(t *testing.T) {
	now := time.Now()
	hourAgo := now.Add(-time.Hour)
	twoHoursAgo := now.Add(-2 * time.Hour)

	toolUses := []ToolUse{
		{Tool: "Bash", CWD: "/Users/moshe/project", Timestamp: &now},
		{Tool: "Read", CWD: "/Users/moshe/project", Timestamp: &hourAgo},
		{Tool: "Write", CWD: "/Users/moshe/other", Timestamp: &twoHoursAgo},
		{Tool: "Edit", CWD: "/Users/moshe/project", Timestamp: &now},
		{Tool: "Grep", CWD: "", Timestamp: &now},
		{Tool: "Write", CWD: "/Users/moshe/project", Input: map[string]any{"file_path": "/Users/moshe/.claude/plans/foo.md"}, Timestamp: &now},
		{Tool: "Write", CWD: "/Users/moshe/project", Input: map[string]any{"file_path": "/Users/moshe/project/main.go"}, Timestamp: &hourAgo},
	}

	tests := []struct {
		name     string
		filter   Filter
		expected []string
	}{
		{
			name:     "empty filter matches all",
			filter:   Filter{},
			expected: []string{"Bash", "Read", "Write", "Edit", "Grep", "Write", "Write"},
		},
		{
			name:     "exact tool match",
			filter:   Filter{Tools: []string{"Bash"}},
			expected: []string{"Bash"},
		},
		{
			name:     "wildcard tool match",
			filter:   Filter{Tools: []string{"*"}},
			expected: []string{"Bash", "Read", "Write", "Edit", "Grep", "Write", "Write"},
		},
		{
			name:     "suffix pattern",
			filter:   Filter{Tools: []string{"*rite"}},
			expected: []string{"Write", "Write", "Write"},
		},
		{
			name:     "negation pattern",
			filter:   Filter{Tools: []string{"!Read"}},
			expected: []string{"Bash", "Write", "Edit", "Grep", "Write", "Write"},
		},
		{
			name:     "multiple tools",
			filter:   Filter{Tools: []string{"Bash", "Read"}},
			expected: []string{"Bash", "Read"},
		},
		{
			name:     "dir filter - exact",
			filter:   Filter{Dirs: []string{"/Users/moshe/project"}},
			expected: []string{"Bash", "Read", "Edit", "Grep", "Write"},
		},
		{
			name:     "dir filter - prefix wildcard",
			filter:   Filter{Dirs: []string{"*/project"}},
			expected: []string{"Bash", "Read", "Edit", "Grep", "Write"},
		},
		{
			name:     "dir filter - negation",
			filter:   Filter{Dirs: []string{"!/Users/moshe/other"}},
			expected: []string{"Bash", "Read", "Edit", "Grep", "Write", "Write"},
		},
		{
			name:     "combined tool and dir",
			filter:   Filter{Tools: []string{"Bash", "Read"}, Dirs: []string{"/Users/moshe/project"}},
			expected: []string{"Bash", "Read"},
		},
		{
			name:     "time filter - since",
			filter:   Filter{Since: &hourAgo},
			expected: []string{"Bash", "Read", "Edit", "Grep", "Write", "Write"},
		},
		{
			name:     "dir filter prefers file_path over CWD",
			filter:   Filter{Tools: []string{"Write"}, Dirs: []string{"/Users/moshe/project"}},
			expected: []string{"Write"},
		},
		{
			name:     "limit returns most recent first",
			filter:   Filter{Limit: 2},
			expected: []string{"Bash", "Edit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterToolUses(toolUses, tt.filter)
			got := make([]string, len(result))
			for i, tu := range result {
				got[i] = tu.Tool
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestExtractToolUses_ExtractsCWD(t *testing.T) {
	entries := []HistoryEntry{
		{
			Timestamp: "2024-01-01T10:00:00Z",
			Message: Message{
				Content: []ContentBlock{
					{
						Type:  ContentTypeToolUse,
						Name:  "Bash",
						Input: []byte(`{"command":"ls","cwd":"/Users/test/project"}`),
					},
				},
			},
		},
		{
			Timestamp: "2024-01-01T10:01:00Z",
			Message: Message{
				Content: []ContentBlock{
					{
						Type:  ContentTypeToolUse,
						Name:  "Read",
						Input: []byte(`{"file_path":"/tmp/test.txt"}`),
					},
				},
			},
		},
	}

	result := ExtractToolUses(entries)

	assert.Len(t, result, 2)
	assert.Equal(t, "/Users/test/project", result[0].CWD)
	assert.Equal(t, "", result[1].CWD)
}
