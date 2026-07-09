package cli

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/claude"
)

func TestAggregateSessionThroughputSeparatesModelEffort(t *testing.T) {
	base := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	records := []SessionRecord{
		throughputRecord("a", "claude", "claude-sonnet-4-6", "low", base, 10*time.Second, 100, 50, 25, 0, 500, 1000),
		throughputRecord("b", "claude", "claude-sonnet-4-6", "low", base.Add(time.Minute), 20*time.Second, 200, 100, 0, 0, 250, 1000),
		throughputRecord("c", "claude", "claude-sonnet-4-6", "high", base.Add(2*time.Minute), 10*time.Second, 100, 200, 0, 0, 800, 1000),
	}

	groups, skipped := aggregateSessionThroughput(records)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}

	high := findThroughputGroup(t, groups, "claude|claude-sonnet-4-6|high")
	low := findThroughputGroup(t, groups, "claude|claude-sonnet-4-6|low")

	assertFloat(t, high.OutputTokensPerSecond, 20)
	assertFloat(t, high.TotalTokensPerSecond, 30)
	assertFloat(t, high.ContextTokensPerSecond, 80)
	assertFloat(t, high.AvgContextUsedPercent, 80)

	assertFloat(t, low.OutputTokensPerSecond, 5)
	assertFloat(t, low.TotalTokensPerSecond, 475.0/30.0)
	assertFloat(t, low.ContextTokensPerSecond, 25)
	assertFloat(t, low.AvgContextUsedPercent, 37.5)
	if low.Sessions != 2 {
		t.Fatalf("low sessions = %d, want 2", low.Sessions)
	}
	if len(low.Points) != 2 || !low.Points[0].At.Before(low.Points[1].At) {
		t.Fatalf("low points are not sorted oldest-first: %+v", low.Points)
	}
}

func TestAggregateSessionThroughputSkipsIncompleteSessions(t *testing.T) {
	base := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	valid := throughputRecord("valid", "codex", "gpt-5", "", base, 10*time.Second, 10, 20, 0, 0, 0, 0)
	noEnd := valid
	noEnd.ID = "no-end"
	noEnd.EndedAt = nil
	noTokens := valid
	noTokens.ID = "no-tokens"
	noTokens.Tokens = nil
	zeroDuration := valid
	zeroDuration.ID = "zero-duration"
	zeroDuration.EndedAt = zeroDuration.StartedAt

	groups, skipped := aggregateSessionThroughput([]SessionRecord{valid, noEnd, noTokens, zeroDuration})
	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3", skipped)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	group := groups[0]
	if group.Key != "codex|gpt-5|default" {
		t.Fatalf("group key = %q, want default effort key", group.Key)
	}
	assertFloat(t, group.OutputTokensPerSecond, 2)
	assertFloat(t, group.TotalTokensPerSecond, 3)
}

func TestRunSessionThroughputRestrictsExplicitProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	otherProject := filepath.Join(home, "work", "other")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherProject, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	markProjectRoot(t, project)
	markProjectRoot(t, otherProject)

	writeThroughputClaudeSession(t, home, project, "sess-current", "2026-06-01T10:00:00Z", "2026-06-01T10:00:10Z")
	writeThroughputClaudeSession(t, home, otherProject, "sess-other", "2026-06-01T10:01:00Z", "2026-06-01T10:01:10Z")

	result, err := RunSessionThroughput(context.Background(), SessionThroughputOptions{
		Source:  "claude",
		Project: otherProject,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("RunSessionThroughput: %v", err)
	}
	if result.Scope != "project" || result.Project != otherProject {
		t.Fatalf("scope/project = %q/%q, want project/%q", result.Scope, result.Project, otherProject)
	}
	if result.Total != 1 {
		t.Fatalf("total = %d, want 1 (result=%+v)", result.Total, result)
	}
	group := findThroughputGroup(t, result.Groups, "claude|claude-sonnet-4|default")
	if group.Sessions != 1 || len(group.Points) != 1 || group.Points[0].SessionID != "sess-other" {
		t.Fatalf("throughput group = %+v", group)
	}
}

func writeThroughputClaudeSession(t *testing.T, home, project, id, started, ended string) {
	t.Helper()
	writeJSONL(t, filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), id+".jsonl"),
		map[string]any{
			"type":      "user",
			"sessionId": id,
			"timestamp": started,
			"cwd":       project,
			"message": map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "measure"}},
			},
		},
		map[string]any{
			"type":      "assistant",
			"sessionId": id,
			"timestamp": ended,
			"cwd":       project,
			"message": map[string]any{
				"role":    "assistant",
				"model":   "claude-sonnet-4",
				"content": []any{map[string]any{"type": "text", "text": "done"}},
				"usage": map[string]any{
					"input_tokens":  100,
					"output_tokens": 50,
				},
			},
		},
	)
}

func findThroughputGroup(t *testing.T, groups []SessionThroughputGroup, key string) SessionThroughputGroup {
	t.Helper()
	for _, group := range groups {
		if group.Key == key {
			return group
		}
	}
	t.Fatalf("group %q not found in %+v", key, groups)
	return SessionThroughputGroup{}
}

func throughputRecord(id, source, model, effort string, start time.Time, duration time.Duration, input, output, cacheRead, cacheCreate, contextUsed, contextWindow int) SessionRecord {
	end := start.Add(duration)
	rec := SessionRecord{
		ID:              id,
		Source:          source,
		Model:           model,
		ReasoningEffort: effort,
		StartedAt:       &start,
		EndedAt:         &end,
		Tokens: &SessionTokensWire{
			InputTokens:         input,
			OutputTokens:        output,
			CacheReadTokens:     cacheRead,
			CacheCreationTokens: cacheCreate,
			TotalTokens:         input + output + cacheRead + cacheCreate,
		},
	}
	if contextUsed > 0 || contextWindow > 0 {
		rec.Context = &SessionContextWire{
			UsedTokens:   contextUsed,
			WindowTokens: contextWindow,
		}
	}
	return rec
}

func assertFloat(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("got %f, want %f", got, want)
	}
}
