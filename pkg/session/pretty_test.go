package session

import (
	"strings"
	"testing"
	"time"
)

func TestSessionPretty_RendersSummaryHistoryFilesAndTranscript(t *testing.T) {
	ts := time.Date(2026, 7, 8, 11, 19, 57, 0, time.UTC)
	end := ts.Add(3 * time.Second)
	s := &Session{
		ID:          "sess-rollout",
		Source:      "codex",
		CWD:         "/repo",
		Model:       "gpt-5",
		HistoryFile: "/tmp/root.jsonl",
		StartedAt:   &ts,
		EndedAt:     &end,
		Root:        &Agent{ID: "sess-rollout", IsRoot: true, HistoryFile: "/tmp/root.jsonl"},
		Agents: []*Agent{
			{ID: "sess-rollout", IsRoot: true, HistoryFile: "/tmp/root.jsonl"},
			{ID: "agent-1", Type: "explorer", Desc: "inspect sessions", HistoryFile: "/tmp/agent.jsonl"},
		},
		Messages: []Message{
			{
				Role:  "user",
				Parts: []Part{{Type: PartText, Text: "show history"}},
				Provenance: &Provenance{
					Timestamp: &ts,
				},
			},
			{
				Role:  "assistant",
				Parts: []Part{{Type: PartText, Text: "done"}},
				Provenance: &Provenance{
					Timestamp: &end,
					AgentID:   "agent-1",
				},
			},
		},
		Events: []Event{{Type: "task_started", Scope: "session", Timestamp: &ts}},
	}

	got := s.Pretty().String()
	for _, want := range []string{
		"Summary",
		"History Files",
		"Transcript",
		"/tmp/root.jsonl",
		"/tmp/agent.jsonl",
		"show history",
		"task started",
		"inspect sessions",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Pretty() missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "│Time") || strings.Contains(got, "│Type") {
		t.Fatalf("Pretty() rendered transcript as a table:\n%s", got)
	}
}
