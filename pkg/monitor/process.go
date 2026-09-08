package monitor

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	sessionprocess "github.com/flanksource/captain/pkg/session/process"
	"github.com/google/uuid"
)

// Process is one live claude/codex OS process observed via ps.
type Process struct {
	Source        string
	PID           int
	Status        string
	CPUPercent    float64
	MemoryPercent float64
	MemoryRSSKB   int64
	StartedAt     *time.Time
	CWD           string
	Command       string
}

// pollProcesses is one monitor tick: sample agent processes, bind each to a
// session, persist the metrics snapshot, close vanished process rows, and feed
// the live transcript locations to the watcher (nil in one-shot runs).
func (m *Monitor) pollProcesses(ctx context.Context, watcher *transcriptWatcher) error {
	processes, err := m.cfg.DiscoverProcesses()
	if err != nil {
		return err
	}
	sampledAt := time.Now().UTC()
	if len(processes) > 0 {
		m.noteActivity(sampledAt)
	}
	alive := make([]int64, 0, len(processes))
	for _, proc := range processes {
		alive = append(alive, int64(proc.PID))
		sessionID, err := m.resolveProcessSession(ctx, proc)
		if err != nil {
			log.Warnf("resolve session for pid %d: %v", proc.PID, err)
			continue
		}
		if err := m.persistProcess(ctx, sessionID, proc, sampledAt); err != nil {
			log.Warnf("persist process pid %d: %v", proc.PID, err)
			continue
		}
		if watcher != nil {
			m.watchLiveSession(ctx, watcher, proc, sessionID)
		}
	}
	if _, err := m.db.EndVanishedProcesses(ctx, m.cfg.HostID, alive); err != nil {
		return err
	}
	if watcher != nil {
		for path, source := range m.trackedPaths() {
			watcher.track(path, source)
		}
	}
	return nil
}

// resolveProcessSession binds a process to a session, in precedence order: the
// authoritative argv session id; the previously recorded binding (unless it
// still points at a provisional stub); the newest ingested session in the same
// working directory; finally a provisional session that later transcript
// ingest fills in.
func (m *Monitor) resolveProcessSession(ctx context.Context, proc Process) (uuid.UUID, error) {
	if providerSessionID := sessionprocess.SessionIDFromCommand(proc.Command); providerSessionID != "" {
		session, err := m.db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerSessionID, Source: proc.Source, HostID: m.cfg.HostID, CWD: proc.CWD,
		})
		if err != nil {
			return uuid.Nil, err
		}
		return session.ID, nil
	}
	sticky, stickyProvisional, err := m.stickyProcessSession(ctx, proc)
	if err != nil {
		return uuid.Nil, err
	}
	if sticky != uuid.Nil && !stickyProvisional {
		return sticky, nil
	}
	if byCWD, err := m.db.FindSessionIDByCWD(ctx, proc.Source, proc.CWD); err != nil {
		return uuid.Nil, err
	} else if byCWD != uuid.Nil {
		return byCWD, nil
	}
	if sticky != uuid.Nil {
		return sticky, nil // keep the provisional stub until an ingest claims the cwd
	}
	session, err := m.db.CreateOrGetSession(ctx, database.CreateSessionInput{
		Source: proc.Source, HostID: m.cfg.HostID, CWD: proc.CWD,
		Description: "provisional session for live process",
	})
	if err != nil {
		return uuid.Nil, err
	}
	return session.ID, nil
}

// stickyProcessSession returns the process's previously recorded session and
// whether that session is still a provisional stub (no provider identity, no
// transcript) — stubs stay rebindable so a later ingest can claim the process.
func (m *Monitor) stickyProcessSession(ctx context.Context, proc Process) (uuid.UUID, bool, error) {
	existing, err := m.db.FindProcessSessionID(ctx, m.cfg.HostID, bootID(), int64(proc.PID), processStartOrNow(proc))
	if err != nil || existing == uuid.Nil {
		return uuid.Nil, false, err
	}
	session, err := m.db.GetSession(ctx, existing)
	if err != nil {
		return uuid.Nil, false, nil // stale binding to a deleted session: rebind
	}
	return existing, session.ProviderSessionID == "" && session.Path == "", nil
}

func (m *Monitor) persistProcess(ctx context.Context, sessionID uuid.UUID, proc Process, sampledAt time.Time) error {
	startedAt := processStartOrNow(proc)
	input := database.SessionProcessInput{
		SessionID: sessionID, HostID: m.cfg.HostID, BootID: bootID(),
		PID: int64(proc.PID), ProcessStartedAt: startedAt,
		Status: proc.Status, Command: proc.Command, CWD: proc.CWD, Source: proc.Source,
		CPUPercent: proc.CPUPercent, MemoryPercent: proc.MemoryPercent, SampledAt: sampledAt,
	}
	if proc.MemoryRSSKB > 0 {
		rss := proc.MemoryRSSKB * 1024
		input.MemoryRSSBytes = &rss
	}
	// Close a superseded process identity before inserting the observation. This
	// avoids using a predictable unique-key violation as control flow (and the
	// corresponding ERROR log), and also drains legacy rows whose timezone bug
	// left the same PID open under a different start timestamp.
	if err := m.db.EndOtherSessionProcesses(ctx, sessionID, int64(proc.PID), startedAt); err != nil {
		return err
	}
	return m.db.UpsertSessionProcess(ctx, input)
}

// watchLiveSession points the watcher at wherever this live session's
// transcript lives (or will appear): the claude project directory for the
// process cwd, or codex's per-day rollout directory.
func (m *Monitor) watchLiveSession(ctx context.Context, watcher *transcriptWatcher, proc Process, sessionID uuid.UUID) {
	switch proc.Source {
	case "claude":
		if proc.CWD != "" {
			watcher.watchDir(filepath.Join(claude.GetProjectsDir(), claude.NormalizePath(proc.CWD)), "claude")
		}
	case "codex":
		watcher.watchDir(codexDayDir(time.Now()), "codex")
	}
	if session, err := m.db.GetSession(ctx, sessionID); err == nil && session.Path != "" {
		watcher.track(session.Path, proc.Source)
	}
}

func codexDayDir(now time.Time) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions", now.Format("2006/01/02"))
}

func processStartOrNow(proc Process) time.Time {
	if proc.StartedAt != nil {
		return proc.StartedAt.UTC()
	}
	return time.Now().UTC()
}

// bootID is a stable-enough boot discriminator for the process identity key.
// PID + start time disambiguate across reboots in practice; a real boot id can
// replace this without a schema change.
func bootID() string { return "boot" }

func discoverAgentProcesses() ([]Process, error) {
	agents, err := sessionprocess.Discover(context.Background())
	if err != nil {
		return nil, err
	}
	processes := make([]Process, len(agents))
	for i, agent := range agents {
		processes[i] = Process{
			Source: agent.Source, PID: agent.PID, Status: agent.Status,
			CPUPercent: agent.CPUPercent, MemoryPercent: agent.MemoryPercent,
			MemoryRSSKB: int64(agent.RSSBytes / 1024), StartedAt: agent.StartedAt,
			CWD: agent.CWD, Command: agent.Command,
		}
	}
	return processes, nil
}
