package cli

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
)

func TestMostRecentSession(t *testing.T) {
	now := time.Now()
	hourAgo := now.Add(-time.Hour)
	dayAgo := now.Add(-24 * time.Hour)

	uses := []claude.ToolUse{
		{SessionID: "old", Timestamp: &dayAgo},
		{SessionID: "recent", Timestamp: &now},
		{SessionID: "old", Timestamp: &hourAgo},
	}

	assert.Equal(t, "recent", mostRecentSession(uses))
}

func TestBuildChangesResult_AggregatesWritePaths(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Minute)
	root := "/home/ubuntu/project"

	uses := []claude.ToolUse{
		{Tool: "Write", Source: "claude", ProjectRoot: root, Timestamp: &earlier,
			Input: map[string]any{"file_path": root + "/main.go"}},
		{Tool: "Edit", Source: "claude", ProjectRoot: root, Timestamp: &now,
			Input: map[string]any{"file_path": root + "/main.go"}},
		{Tool: "Read", Source: "claude", ProjectRoot: root, Timestamp: &now,
			Input: map[string]any{"file_path": root + "/readme.md"}},
		{Tool: "Write", Source: "claude", ProjectRoot: root, Timestamp: &now,
			Input: map[string]any{"file_path": root + "/util.go"}},
	}

	result := buildChangesResult("sess-1", uses)

	assert.Equal(t, "sess-1", result.SessionID)
	assert.Equal(t, "claude", result.Source)
	// Only main.go (Write+Edit) and util.go (Write) count; readme.md was only Read.
	assert.Equal(t, 2, result.FileCount)

	// main.go has the most edits, so it sorts first.
	assert.Equal(t, "main.go", result.Files[0].Path)
	assert.Equal(t, 2, result.Files[0].Edits)
	assert.Equal(t, "Edit, Write", result.Files[0].Tools)
	assert.Equal(t, now.Format("2006-01-02 15:04"), result.Files[0].Last)

	assert.Equal(t, "util.go", result.Files[1].Path)
	assert.Equal(t, 1, result.Files[1].Edits)
}
