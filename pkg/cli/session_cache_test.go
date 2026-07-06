package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// claudeSummaryRow is a minimal on-disk claude session line carrying the given
// session id, enough for the fast summarizer to extract an ID.
func claudeSummaryRow(id string) map[string]any {
	return map[string]any{
		"type":      "user",
		"sessionId": id,
		"timestamp": "2026-06-01T10:00:00Z",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": "hi"}},
		},
	}
}

// TestSummarizeSessionFileCachedDegradesUncached checks the store-disabled path
// (TestMain sets CAPTAIN_SESSION_DB_URL=off): summarization still works, uses the
// in-file session id, and reflects the current content on every call (no stale
// cache). Cache reuse/invalidation against a real DB is covered by the gated
// integration test.
func TestSummarizeSessionFileCachedDegradesUncached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aa.jsonl")
	ref := sessionFileRef{source: "claude", path: path}

	writeJSONL(t, path, claudeSummaryRow("aa"))
	first, err := summarizeSessionFileCached(ref)
	if err != nil {
		t.Fatalf("first summarize: %v", err)
	}
	if first.ID != "aa" {
		t.Fatalf("first.ID = %q, want aa (from the in-file sessionId)", first.ID)
	}

	// With no store, a rewritten file is re-read every time (uncached).
	writeJSONL(t, path, claudeSummaryRow("bb"))
	again, err := summarizeSessionFileCached(ref)
	if err != nil {
		t.Fatalf("second summarize: %v", err)
	}
	if again.ID != "bb" {
		t.Fatalf("second.ID = %q, want bb (uncached re-read)", again.ID)
	}
}

func TestSummarizeSessionRefsParallelFindsEverySession(t *testing.T) {
	dir := t.TempDir()
	const count = 60
	refs := make([]sessionFileRef, 0, count)
	want := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("sess-%03d", i)
		path := filepath.Join(dir, id+".jsonl")
		writeJSONL(t, path, claudeSummaryRow(id))
		refs = append(refs, sessionFileRef{source: "claude", path: path})
		want[id] = true
	}

	got := summarizeSessionRefs(context.Background(), refs)
	if len(got) != count {
		t.Fatalf("got %d candidates, want %d", len(got), count)
	}
	seen := make(map[string]bool, count)
	for i, candidate := range got {
		seen[candidate.record.ID] = true
		// Order must match the input refs so downstream sorting is deterministic.
		if candidate.path != refs[i].path {
			t.Fatalf("candidate[%d].path = %q, want %q (order not preserved)", i, candidate.path, refs[i].path)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("session %q missing from parallel summary", id)
		}
	}
}
