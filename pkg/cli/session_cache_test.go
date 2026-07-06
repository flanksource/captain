package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestSummarizeSessionFileCachedReusesUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	fixed := time.Unix(1_700_000_000, 0)
	ref := sessionFileRef{source: "claude", path: path}

	// v1 → cached against the fixed mtime/size.
	writeJSONL(t, path, claudeSummaryRow("aa"))
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	first, err := summarizeSessionFileCached(ref)
	if err != nil {
		t.Fatalf("first summarize: %v", err)
	}
	if first.ID != "aa" {
		t.Fatalf("first.ID = %q, want aa", first.ID)
	}

	// Rewrite to v2 with the SAME byte size and restore the SAME mtime: the cache
	// key is unchanged, so the read must be skipped and the stale record returned.
	writeJSONL(t, path, claudeSummaryRow("bb"))
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	cached, err := summarizeSessionFileCached(ref)
	if err != nil {
		t.Fatalf("cached summarize: %v", err)
	}
	if cached.ID != "aa" {
		t.Fatalf("cached.ID = %q, want stale aa (cache hit expected)", cached.ID)
	}

	// A newer mtime invalidates the entry and re-reads the current content.
	if err := os.Chtimes(path, fixed.Add(time.Minute), fixed.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	fresh, err := summarizeSessionFileCached(ref)
	if err != nil {
		t.Fatalf("fresh summarize: %v", err)
	}
	if fresh.ID != "bb" {
		t.Fatalf("fresh.ID = %q, want bb (cache invalidated)", fresh.ID)
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
