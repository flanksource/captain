package cli

import (
	"reflect"
	"testing"
)

func TestClassifyOpenSessionFile(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		wantSource string
		wantID     string
		wantKind   string
	}{
		{
			name:       "codex rollout",
			path:       "/Users/moshe/.codex/sessions/2026/07/09/rollout-2026-07-09T08-45-27-019f4568-d9a2-76c0-9ab1-2e465c2d5e35.jsonl",
			wantSource: "codex",
			wantID:     "019f4568-d9a2-76c0-9ab1-2e465c2d5e35",
			wantKind:   "codex",
		},
		{
			name:       "claude root",
			path:       "/Users/moshe/.claude/projects/-Users-moshe-work/b3c450b5-aab4-4834-9420-33a1839e15b8.jsonl",
			wantSource: "claude",
			wantID:     "b3c450b5-aab4-4834-9420-33a1839e15b8",
			wantKind:   "root",
		},
		{
			name:       "claude subagent",
			path:       "/Users/moshe/.claude/projects/-Users-moshe-work/b3c450b5/subagents/agent-ae31f6111faba5d7b.jsonl",
			wantSource: "claude",
			wantID:     "ae31f6111faba5d7b",
			wantKind:   "subagent",
		},
		{name: "non-jsonl", path: "/Users/moshe/.codex/logs_2.sqlite"},
		{name: "unrelated jsonl", path: "/Users/moshe/notes/todo.jsonl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, id, kind := classifyOpenSessionFile(tc.path)
			if source != tc.wantSource || id != tc.wantID || kind != tc.wantKind {
				t.Fatalf("classify(%q) = (%q,%q,%q), want (%q,%q,%q)",
					tc.path, source, id, kind, tc.wantSource, tc.wantID, tc.wantKind)
			}
		})
	}
}

func TestParseLsofOpenFiles(t *testing.T) {
	// Two codex rollouts on pid 84650 (main + sub-agent), one claude root on
	// pid 77310; sqlite/dir/socket fds must be filtered out.
	out := []byte("p84650\n" +
		"n/Users/moshe/.codex/logs_2.sqlite\n" +
		"n/Users/moshe/.codex/sessions/2026/07/09/rollout-2026-07-09T08-45-27-019f4568-d9a2-76c0-9ab1-2e465c2d5e35.jsonl\n" +
		"n/Users/moshe/.codex/sessions/2026/07/09/rollout-2026-07-09T08-45-27-019f4568-da0d-7b82-b9f7-f24a2348a699.jsonl\n" +
		"p77310\n" +
		"n/Users/moshe/go/src/project\n" +
		"n/Users/moshe/.claude/projects/-Users-moshe-work/b3c450b5-aab4-4834-9420-33a1839e15b8.jsonl\n")

	files := parseLsofOpenFiles(out)

	wantCodex := []string{
		"/Users/moshe/.codex/sessions/2026/07/09/rollout-2026-07-09T08-45-27-019f4568-d9a2-76c0-9ab1-2e465c2d5e35.jsonl",
		"/Users/moshe/.codex/sessions/2026/07/09/rollout-2026-07-09T08-45-27-019f4568-da0d-7b82-b9f7-f24a2348a699.jsonl",
	}
	if !reflect.DeepEqual(files[84650], wantCodex) {
		t.Fatalf("pid 84650 files = %v", files[84650])
	}
	wantClaude := []string{"/Users/moshe/.claude/projects/-Users-moshe-work/b3c450b5-aab4-4834-9420-33a1839e15b8.jsonl"}
	if !reflect.DeepEqual(files[77310], wantClaude) {
		t.Fatalf("pid 77310 files = %v", files[77310])
	}
}
