package monitor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
)

// CodexEventTurnComplete is the only event codex's notify mechanism emits.
const CodexEventTurnComplete = "agent-turn-complete"

// HookEvent is one normalized real-time signal from a provider hook: a Claude
// Code lifecycle hook (SessionStart/UserPromptSubmit/Stop/SubagentStop/
// SessionEnd) or codex's notify agent-turn-complete. Hooks carry exact session
// identity and transcript location, replacing ps-based discovery for sessions
// that have them installed.
type HookEvent struct {
	Provider       string    `json:"provider,omitempty"` // claude | codex
	Event          string    `json:"event"`
	SessionID      string    `json:"sessionId,omitempty"` // provider session id / codex thread id
	TranscriptPath string    `json:"transcriptPath,omitempty"`
	CWD            string    `json:"cwd,omitempty"`
	Detail         string    `json:"detail,omitempty"` // SessionStart source / SessionEnd reason
	ReceivedAt     time.Time `json:"-"`
}

// NotifyHookEvent enqueues a hook event without blocking. Events are dropped
// (with a debug log) when the buffer is full or no locked run loop is draining
// it — hook delivery is best-effort by design; the startup/daily recon and the
// stale-process reaper converge the database over dropped events.
func (m *Monitor) NotifyHookEvent(ev HookEvent) {
	if ev.ReceivedAt.IsZero() {
		ev.ReceivedAt = time.Now().UTC()
	}
	select {
	case m.hookEvents <- ev:
	default:
		log.Debugf("hook event queue full; dropping %s %s for %s", ev.Provider, ev.Event, ev.SessionID)
	}
}

// handleHookEvent maps one hook event onto monitor actions: SessionStart binds
// the session and arms tailing, activity events trigger a targeted ingest, and
// SessionEnd finalizes the transcript and closes the session's process rows.
func (m *Monitor) handleHookEvent(ctx context.Context, watcher *transcriptWatcher, ingestor *ingestor, ev HookEvent) {
	m.noteActivity(ev.ReceivedAt)
	path := ev.TranscriptPath
	if ev.Provider == "codex" && path == "" {
		path = resolveCodexTranscript(ev.SessionID)
		if path == "" {
			// The rollout may not be flushed yet; arm today's day dir so the
			// eventual write is tailed, and let recon cover the rest.
			log.Debugf("hook codex/%s: no rollout found for thread %s yet", ev.Event, ev.SessionID)
			watcher.watchDir(codexDayDir(time.Now()), "codex")
			return
		}
	}
	if path != "" {
		if err := validateHookTranscript(ev.Provider, path); err != nil {
			log.Warnf("hook %s/%s: %v", ev.Provider, ev.Event, err)
			return
		}
	}

	switch ev.Event {
	case string(claude.HookEventSessionStart):
		if ev.SessionID != "" {
			if _, err := m.db.CreateOrGetSession(ctx, database.CreateSessionInput{
				ProviderSessionID: ev.SessionID, Source: ev.Provider, HostID: m.cfg.HostID,
				CWD: ev.CWD, Path: path,
			}); err != nil {
				log.Warnf("hook %s/SessionStart: bind session %s: %v", ev.Provider, ev.SessionID, err)
			}
		}
		if path != "" {
			m.TrackTranscript(path, ev.Provider)
			watcher.track(path, ev.Provider)
		}
	case string(claude.HookEventSessionEnd):
		if path != "" {
			if err := ingestor.ingestFile(ctx, ev.Provider, path); err != nil {
				log.Warnf("hook %s/SessionEnd: ingest %s: %v", ev.Provider, path, err)
			}
			m.untrackTranscript(path)
		}
		m.endHookSessionProcesses(ctx, ev)
	default:
		// UserPromptSubmit / Stop / SubagentStop / agent-turn-complete: the
		// session made progress — tail it and ingest what is on disk now.
		// ingestFile is idempotent, so overlap with fsnotify-triggered ingest
		// of the same file is safe.
		if path != "" {
			m.TrackTranscript(path, ev.Provider)
			watcher.track(path, ev.Provider)
			if err := ingestor.ingestFile(ctx, ev.Provider, path); err != nil {
				log.Warnf("hook %s/%s: ingest %s: %v", ev.Provider, ev.Event, path, err)
			}
		}
	}
}

// endHookSessionProcesses closes the open process rows of the session named by
// a SessionEnd event. For reason=clear/resume the OS process lives on under a
// successor session id; the next process poll rebinds it.
func (m *Monitor) endHookSessionProcesses(ctx context.Context, ev HookEvent) {
	if ev.SessionID == "" {
		return
	}
	session, err := m.db.CreateOrGetSession(ctx, database.CreateSessionInput{
		ProviderSessionID: ev.SessionID, Source: ev.Provider, HostID: m.cfg.HostID, CWD: ev.CWD,
	})
	if err != nil {
		log.Warnf("hook %s/SessionEnd: resolve session %s: %v", ev.Provider, ev.SessionID, err)
		return
	}
	if _, err := m.db.EndSessionProcesses(ctx, session.ID); err != nil {
		log.Warnf("hook %s/SessionEnd: close processes for %s: %v", ev.Provider, ev.SessionID, err)
	}
}

// validateHookTranscript rejects transcript paths outside the provider's known
// session roots. Hook events arrive over an unauthenticated localhost endpoint;
// the monitor must never ingest arbitrary files.
func validateHookTranscript(provider, path string) error {
	root, err := hookTranscriptRoot(provider)
	if err != nil {
		return err
	}
	if root == "" {
		return fmt.Errorf("no session root for provider %q", provider)
	}
	if !strings.HasPrefix(filepath.Clean(path), root+string(filepath.Separator)) {
		return fmt.Errorf("transcript %s is outside %s", path, root)
	}
	return nil
}

func hookTranscriptRoot(provider string) (string, error) {
	switch provider {
	case "claude":
		return claude.GetProjectsDir(), nil
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".codex", "sessions"), nil
	default:
		return "", fmt.Errorf("unknown hook provider %q", provider)
	}
}

// resolveCodexTranscript finds the rollout file for a codex thread id. The
// notify payload carries no path, but rollout filenames end with the thread id
// (rollout-<timestamp>-<thread-id>.jsonl); check the recent day directories
// first, then fall back to the full sessions scan for long-running threads.
func resolveCodexTranscript(threadID string) string {
	if threadID == "" {
		return ""
	}
	suffix := "-" + threadID + ".jsonl"
	now := time.Now()
	for _, day := range []time.Time{now, now.AddDate(0, 0, -1)} {
		dir := codexDayDir(day)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), suffix) {
				return filepath.Join(dir, entry.Name())
			}
		}
	}
	files, err := history.FindCodexSessionFiles()
	if err != nil {
		return ""
	}
	for _, file := range files {
		if strings.HasSuffix(filepath.Base(file), suffix) {
			return file
		}
	}
	return ""
}
