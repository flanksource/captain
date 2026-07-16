package monitor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextPollInterval(t *testing.T) {
	m := &Monitor{cfg: Config{ProcessInterval: 5 * time.Second, IdleProcessInterval: time.Minute}}

	assert.Equal(t, time.Minute, m.nextPollInterval(), "no activity ever observed must poll at the idle cadence")

	m.noteActivity(time.Now())
	assert.Equal(t, 5*time.Second, m.nextPollInterval(), "recent activity must poll at the fast cadence")

	m.lastActivity = time.Now().Add(-activityWindow - time.Second)
	assert.Equal(t, time.Minute, m.nextPollInterval(), "activity older than the window must poll at the idle cadence")

	m.noteActivity(m.lastActivity.Add(-time.Minute))
	assert.Equal(t, time.Minute, m.nextPollInterval(), "noteActivity must never move lastActivity backwards")
}

func TestNotifyHookEventNeverBlocks(t *testing.T) {
	m := &Monitor{cfg: Config{ProcessInterval: 5 * time.Second, IdleProcessInterval: time.Minute}, hookEvents: make(chan HookEvent, 2)}
	for range 5 {
		m.NotifyHookEvent(HookEvent{Provider: "claude", Event: "Stop", SessionID: "s"})
	}
	assert.Len(t, m.hookEvents, 2, "overflow events must be dropped, not block")
}

func TestValidateHookTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	claudePath := filepath.Join(home, ".claude", "projects", "-repo", "s.jsonl")
	codexPath := filepath.Join(home, ".codex", "sessions", "2026", "07", "14", "rollout-x.jsonl")

	assertValid := func(provider, path string) {
		t.Helper()
		got, err := validateHookTranscript(provider, path)
		assert.NoError(t, err)
		assert.Equal(t, filepath.Clean(path), got, "must return the cleaned, contained path")
	}
	assertRejected := func(provider, path, msg string) {
		t.Helper()
		got, err := validateHookTranscript(provider, path)
		assert.Error(t, err, msg)
		assert.Empty(t, got, "rejected paths must not be returned")
	}

	assertValid("claude", claudePath)
	assertValid("codex", codexPath)

	assertRejected("claude", "/etc/passwd", "absolute outside path must be rejected")
	assertRejected("claude", codexPath, "codex path must not pass as claude")
	assertRejected("claude",
		filepath.Join(home, ".claude", "projects", "..", "..", "secret.jsonl"), "traversal must be rejected")
	assertRejected("gemini", claudePath, "unknown provider must be rejected")
}

func TestResolveCodexTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	threadID := "0195c1de-4ab8-7000-8000-0123456789ab"

	assert.Empty(t, resolveCodexTranscript(threadID), "no sessions dir yet")
	assert.Empty(t, resolveCodexTranscript(""), "empty thread id")

	writeRollout := func(day time.Time, name string) string {
		dir := codexDayDir(day)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o644))
		return path
	}

	today := writeRollout(time.Now(), "rollout-2026-07-14T10-00-00-"+threadID+".jsonl")
	writeRollout(time.Now(), "rollout-2026-07-14T09-00-00-other-thread.jsonl")
	assert.Equal(t, today, resolveCodexTranscript(threadID))

	oldThread := "0195c1de-4ab8-7000-8000-00000000cafe"
	old := writeRollout(time.Now().AddDate(0, 0, -10), "rollout-2026-07-04T10-00-00-"+oldThread+".jsonl")
	assert.Equal(t, old, resolveCodexTranscript(oldThread), "threads outside the recent day dirs resolve via the full scan")
}
