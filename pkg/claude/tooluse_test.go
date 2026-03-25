package claude

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFilterToolUses(t *testing.T) {
	now := time.Now()
	hourAgo := now.Add(-time.Hour)
	twoHoursAgo := now.Add(-2 * time.Hour)

	toolUses := []ToolUse{
		{Tool: "Bash", CWD: "/home/ubuntu/project", Timestamp: &now},
		{Tool: "Read", CWD: "/home/ubuntu/project", Timestamp: &hourAgo},
		{Tool: "Write", CWD: "/home/ubuntu/other", Timestamp: &twoHoursAgo},
		{Tool: "Edit", CWD: "/home/ubuntu/project", Timestamp: &now},
		{Tool: "Grep", CWD: "", Timestamp: &now},
		{Tool: "Write", CWD: "/home/ubuntu/project", Input: map[string]any{"file_path": "/home/ubuntu/.claude/plans/foo.md"}, Timestamp: &now},
		{Tool: "Write", CWD: "/home/ubuntu/project", Input: map[string]any{"file_path": "/home/ubuntu/project/main.go"}, Timestamp: &hourAgo},
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
			filter:   Filter{Dirs: []string{"/home/ubuntu/project"}},
			expected: []string{"Bash", "Read", "Edit", "Grep", "Write"},
		},
		{
			name:     "dir filter - prefix wildcard",
			filter:   Filter{Dirs: []string{"*/project"}},
			expected: []string{"Bash", "Read", "Edit", "Grep", "Write"},
		},
		{
			name:     "dir filter - negation",
			filter:   Filter{Dirs: []string{"!/home/ubuntu/other"}},
			expected: []string{"Bash", "Read", "Edit", "Grep", "Write", "Write"},
		},
		{
			name:     "combined tool and dir",
			filter:   Filter{Tools: []string{"Bash", "Read"}, Dirs: []string{"/home/ubuntu/project"}},
			expected: []string{"Bash", "Read"},
		},
		{
			name:     "time filter - since",
			filter:   Filter{Since: &hourAgo},
			expected: []string{"Bash", "Read", "Edit", "Grep", "Write", "Write"},
		},
		{
			name:     "dir filter prefers file_path over CWD",
			filter:   Filter{Tools: []string{"Write"}, Dirs: []string{"/home/ubuntu/project"}},
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

func TestExtractToolUses_DetectsDenials(t *testing.T) {
	denialContent, _ := json.Marshal("The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). To tell you how to proceed, the user said:\nkeep commons/logger")

	entries := []HistoryEntry{
		{
			Timestamp: "2024-01-01T10:00:00Z",
			Message: Message{
				Role: MessageRoleAssistant,
				Content: []ContentBlock{
					{Type: ContentTypeToolUse, ID: "tu-1", Name: "Edit", Input: []byte(`{"file_path":"/tmp/a.go"}`)},
					{Type: ContentTypeToolUse, ID: "tu-2", Name: "Bash", Input: []byte(`{"command":"ls"}`)},
				},
			},
		},
		{
			Timestamp: "2024-01-01T10:00:01Z",
			Message: Message{
				Role: MessageRoleUser,
				Content: []ContentBlock{
					{Type: ContentTypeToolResult, ToolUseID: "tu-1", IsError: true, Content: denialContent},
					{Type: ContentTypeToolResult, ToolUseID: "tu-2", Content: []byte(`"ok"`)},
				},
			},
		},
	}

	result := ExtractToolUses(entries)
	assert.Len(t, result, 2)

	assert.True(t, result[0].Denied)
	assert.Equal(t, "keep commons/logger", result[0].DeniedReason)

	assert.False(t, result[1].Denied)
	assert.Empty(t, result[1].DeniedReason)
}

func TestExtractToolUses_DenialWithArrayContent(t *testing.T) {
	entries := []HistoryEntry{
		{
			Timestamp: "2024-01-01T10:00:00Z",
			Message: Message{
				Role: MessageRoleAssistant,
				Content: []ContentBlock{
					{Type: ContentTypeToolUse, ID: "tu-1", Name: "Write", Input: []byte(`{"file_path":"/tmp/b.go"}`)},
				},
			},
		},
		{
			Timestamp: "2024-01-01T10:00:01Z",
			Message: Message{
				Role: MessageRoleUser,
				Content: []ContentBlock{
					{
						Type:      ContentTypeToolResult,
						ToolUseID: "tu-1",
						IsError:   true,
						Content:   []byte(`[{"type":"text","text":"The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). To tell you how to proceed, the user said:\nuse a different approach"}]`),
					},
				},
			},
		},
	}

	result := ExtractToolUses(entries)
	assert.Len(t, result, 1)
	assert.True(t, result[0].Denied)
	assert.Equal(t, "use a different approach", result[0].DeniedReason)
}

func TestExtractToolUses_ErrorNotDenial(t *testing.T) {
	entries := []HistoryEntry{
		{
			Timestamp: "2024-01-01T10:00:00Z",
			Message: Message{
				Role: MessageRoleAssistant,
				Content: []ContentBlock{
					{Type: ContentTypeToolUse, ID: "tu-1", Name: "Bash", Input: []byte(`{"command":"false"}`)},
				},
			},
		},
		{
			Timestamp: "2024-01-01T10:00:01Z",
			Message: Message{
				Role: MessageRoleUser,
				Content: []ContentBlock{
					{Type: ContentTypeToolResult, ToolUseID: "tu-1", IsError: true, Content: []byte(`"exit code 1"`)},
				},
			},
		},
	}

	result := ExtractToolUses(entries)
	assert.Len(t, result, 1)
	assert.False(t, result[0].Denied)
}

func TestExtractToolUsesWithTokens(t *testing.T) {
	entries := []HistoryEntry{
		{
			Timestamp: "2024-01-01T10:00:00Z",
			Message: Message{
				Role: MessageRoleAssistant,
				Content: []ContentBlock{
					{Type: ContentTypeToolUse, ID: "tu-1", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/foo.go"}`)},
					{Type: ContentTypeToolUse, ID: "tu-2", Name: "Bash", Input: json.RawMessage(`{"command":"ls -la"}`)},
				},
			},
		},
		{
			Timestamp: "2024-01-01T10:00:01Z",
			Message: Message{
				Role: MessageRoleUser,
				Content: []ContentBlock{
					{Type: ContentTypeToolResult, ToolUseID: "tu-1", Content: json.RawMessage(`"package main\nfunc main() {}\\n"`)},
					{Type: ContentTypeToolResult, ToolUseID: "tu-2", Content: json.RawMessage(`"total 42\ndrwxr-xr-x 5 user staff 160"`)},
				},
			},
		},
	}

	result := ExtractToolUsesWithTokens(entries)
	assert.Len(t, result, 2)

	// Both should have estimated input and output tokens
	assert.Greater(t, result[0].InputTokens, 0)
	assert.Greater(t, result[0].OutputTokens, 0)
	assert.False(t, result[0].IsError)

	assert.Greater(t, result[1].InputTokens, 0)
	assert.Greater(t, result[1].OutputTokens, 0)
	assert.False(t, result[1].IsError)
}

func TestExtractToolUsesWithTokens_Error(t *testing.T) {
	entries := []HistoryEntry{
		{
			Timestamp: "2024-01-01T10:00:00Z",
			Message: Message{
				Role: MessageRoleAssistant,
				Content: []ContentBlock{
					{Type: ContentTypeToolUse, ID: "tu-1", Name: "Bash", Input: json.RawMessage(`{"command":"false"}`)},
				},
			},
		},
		{
			Timestamp: "2024-01-01T10:00:01Z",
			Message: Message{
				Role: MessageRoleUser,
				Content: []ContentBlock{
					{Type: ContentTypeToolResult, ToolUseID: "tu-1", IsError: true, Content: json.RawMessage(`"exit code 1"`)},
				},
			},
		},
	}

	result := ExtractToolUsesWithTokens(entries)
	assert.Len(t, result, 1)
	assert.True(t, result[0].IsError)
	assert.Greater(t, result[0].OutputTokens, 0)
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
