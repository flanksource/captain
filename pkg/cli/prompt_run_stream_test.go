package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/session"
)

func TestRunStream_ReplayThenLive(t *testing.T) {
	s := newRunStream()
	s.publish(session.Message{ID: "a"})
	s.publish(session.Message{ID: "b"})

	replay, ch, done, _, _ := s.subscribe()
	if done {
		t.Fatal("stream should not be done")
	}
	if len(replay) != 2 || replay[0].ID != "a" || replay[1].ID != "b" {
		t.Fatalf("replay = %+v, want [a b]", replay)
	}

	s.publish(session.Message{ID: "c"})
	select {
	case e := <-ch:
		if e.ID != "c" {
			t.Fatalf("live frame = %q, want c", e.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive live frame")
	}

	s.complete(PromptRunSummary{RunID: "r", Success: true})
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after complete")
	}
}

func TestRunStream_SubscribeAfterDone(t *testing.T) {
	s := newRunStream()
	s.publish(session.Message{ID: "a"})
	s.complete(PromptRunSummary{RunID: "r", Success: true, Duration: "1s"})

	replay, ch, done, summary, errMsg := s.subscribe()
	if !done || ch != nil {
		t.Fatalf("subscribe after done: done=%v ch=%v, want done + nil ch", done, ch)
	}
	if len(replay) != 1 {
		t.Fatalf("replay len = %d, want 1", len(replay))
	}
	if summary == nil || !summary.Success {
		t.Fatalf("summary = %+v, want Success", summary)
	}
	if errMsg != "" {
		t.Fatalf("errMsg = %q, want empty", errMsg)
	}
}

func TestRunStream_Fail(t *testing.T) {
	s := newRunStream()
	s.fail("boom")
	_, _, done, summary, errMsg := s.subscribe()
	if !done || summary != nil || errMsg != "boom" {
		t.Fatalf("fail state: done=%v summary=%v err=%q", done, summary, errMsg)
	}
}

func TestRunStream_DropsSlowSubscriber(t *testing.T) {
	s := newRunStream()
	_, ch, _, _, _ := s.subscribe()

	// Never drain ch; publish more than the buffer. The producer must not block.
	pumped := make(chan struct{})
	go func() {
		for i := 0; i < runSubBuffer+10; i++ {
			s.publish(session.Message{ID: fmt.Sprintf("e%d", i)})
		}
		close(pumped)
	}()
	select {
	case <-pumped:
	case <-time.After(2 * time.Second):
		t.Fatal("producer blocked on a slow subscriber")
	}

	// The subscriber was dropped once full, so ch is closed; range terminates.
	drained := 0
	for range ch {
		drained++
	}
	if drained > runSubBuffer {
		t.Fatalf("drained %d, want <= buffer %d (subscriber should be dropped)", drained, runSubBuffer)
	}
}

func TestRunBroker_PruneRemovesFinishedOldRuns(t *testing.T) {
	b := &runBroker{runs: map[string]*runStream{}}
	b.create("live")
	old := b.create("old")
	old.complete(PromptRunSummary{RunID: "old"})
	old.mu.Lock()
	old.endedAt = time.Now().Add(-time.Hour)
	old.mu.Unlock()

	b.prune(15 * time.Minute)

	if _, ok := b.get("old"); ok {
		t.Fatal("old finished run should be pruned")
	}
	if _, ok := b.get("live"); !ok {
		t.Fatal("live run must be retained")
	}
}

func TestHandlePromptRunSnapshot(t *testing.T) {
	b := &runBroker{runs: map[string]*runStream{}}
	s := b.create("r1")
	s.publish(session.Message{ID: "a"})
	s.complete(PromptRunSummary{RunID: "r1", Success: true, Model: "m"})

	req := httptest.NewRequest(http.MethodGet, "/api/captain/prompt/runs/r1", nil)
	req.SetPathValue("runId", "r1")
	rec := httptest.NewRecorder()
	handlePromptRunSnapshot(b)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body promptRunSnapshotBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Done || len(body.Entries) != 1 || body.Summary == nil || body.Summary.Model != "m" {
		t.Fatalf("snapshot body = %+v", body)
	}
}

func TestHandlePromptRunSnapshot_NotFound(t *testing.T) {
	b := &runBroker{runs: map[string]*runStream{}}
	req := httptest.NewRequest(http.MethodGet, "/api/captain/prompt/runs/none", nil)
	req.SetPathValue("runId", "none")
	rec := httptest.NewRecorder()
	handlePromptRunSnapshot(b)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
