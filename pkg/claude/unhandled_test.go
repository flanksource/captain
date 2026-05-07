package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnhandledStreamTypes(t *testing.T) {
	ResetUnhandledStreamTypes()

	RecordUnhandledStreamType("file-history-snapshot")
	RecordUnhandledStreamType("file-history-snapshot")
	RecordUnhandledStreamType("system/away_summary")
	RecordUnhandledStreamType("")

	snap := SnapshotUnhandledStreamTypes()
	assert.Equal(t, 2, snap["file-history-snapshot"])
	assert.Equal(t, 1, snap["system/away_summary"])
	assert.NotContains(t, snap, "")

	snap["file-history-snapshot"] = 999
	again := SnapshotUnhandledStreamTypes()
	assert.Equal(t, 2, again["file-history-snapshot"], "snapshot should be a copy, not a live reference")

	ResetUnhandledStreamTypes()
	assert.Empty(t, SnapshotUnhandledStreamTypes())
}
