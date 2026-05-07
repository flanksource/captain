package claude

import (
	"maps"
	"sync"
)

var (
	unhandledMu     sync.Mutex
	unhandledStream = make(map[string]int)
)

// RecordUnhandledStreamType increments the counter for a stream-json
// (type, subtype) tuple that the parser does not explicitly handle. Use
// "type" alone when the line has no subtype, or "type/subtype" otherwise.
func RecordUnhandledStreamType(key string) {
	if key == "" {
		return
	}
	unhandledMu.Lock()
	defer unhandledMu.Unlock()
	unhandledStream[key]++
}

// SnapshotUnhandledStreamTypes returns a copy of the unhandled-type counters.
// Safe to call concurrently with RecordUnhandledStreamType.
func SnapshotUnhandledStreamTypes() map[string]int {
	unhandledMu.Lock()
	defer unhandledMu.Unlock()
	out := make(map[string]int, len(unhandledStream))
	maps.Copy(out, unhandledStream)
	return out
}

// ResetUnhandledStreamTypes clears the counter map. Intended for tests.
func ResetUnhandledStreamTypes() {
	unhandledMu.Lock()
	defer unhandledMu.Unlock()
	unhandledStream = make(map[string]int)
}
