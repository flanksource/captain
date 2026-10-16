package monitor

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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
	// ProviderSessionID is the process's own session id read from its
	// environment (see environmentSessionID); empty when it carries none.
	ProviderSessionID string
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
			// The cached binding may name a session deleted since; resolve it
			// afresh next poll.
			m.sessionIDs.forgetSession(sessionID)
			m.samples.forgetSession(sessionID)
			log.Warnf("persist process pid %d: %v", proc.PID, err)
			continue
		}
		if watcher != nil {
			m.watchLiveSession(ctx, watcher, proc, sessionID)
		}
	}
	m.samples.retainPIDs(alive)
	if m.samples.sweepDue(alive) {
		if _, err := m.db.EndVanishedProcesses(ctx, m.cfg.HostID, alive); err != nil {
			return err
		}
		m.samples.markSwept(alive)
	}
	if watcher != nil {
		for path, source := range m.trackedPaths() {
			watcher.track(path, source)
		}
	}
	return nil
}

// resolveProcessSession binds a process to a session, in precedence order: the
// authoritative argv session id; the process's own environment session id; the
// previously recorded binding (unless it still points at a provisional stub);
// the newest ingested session in the same working directory; finally a
// provisional session that later transcript ingest fills in.
func (m *Monitor) resolveProcessSession(ctx context.Context, proc Process) (uuid.UUID, error) {
	providerSessionID := sessionprocess.SessionIDFromCommand(proc.Command)
	if providerSessionID == "" {
		providerSessionID = proc.ProviderSessionID
	}
	if providerSessionID != "" {
		return m.providerSessionID(ctx, database.CreateSessionInput{
			ProviderSessionID: providerSessionID, Source: proc.Source, HostID: m.cfg.HostID, CWD: proc.CWD,
		})
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

// persistProcess writes a process sample only when it says something new: the
// binding changed, or the row's heartbeat is due. The heartbeat interval is the
// idle poll cadence, so a row is never staler than an idle monitor would leave
// it, and far inside staleProcessCutoff.
func (m *Monitor) persistProcess(ctx context.Context, sessionID uuid.UUID, proc Process, sampledAt time.Time) error {
	startedAt := processStartOrNow(proc)
	key := processKey{pid: int64(proc.PID), startedAt: startedAt}
	binding := processBinding{
		sessionID: sessionID, status: proc.Status, command: proc.Command, cwd: proc.CWD, source: proc.Source,
	}
	last, seen := m.samples.lastWritten(key)
	if seen && last.binding == binding && sampledAt.Sub(last.writtenAt) < m.cfg.IdleProcessInterval {
		return nil
	}
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
	// left the same PID open under a different start timestamp. It runs on every
	// write, not only on a new binding: another live process bound to the same
	// session may have closed this row since, and the upsert reopens it.
	if err := m.db.EndOtherSessionProcesses(ctx, sessionID, int64(proc.PID), startedAt); err != nil {
		return err
	}
	if err := m.db.UpsertSessionProcess(ctx, input); err != nil {
		return err
	}
	m.samples.record(key, processSample{binding: binding, writtenAt: sampledAt})
	return nil
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
	path := m.samples.sessionPath(sessionID)
	if path == "" {
		if session, err := m.db.GetSession(ctx, sessionID); err == nil {
			path = session.Path
			m.samples.rememberPath(sessionID, path)
		}
	}
	if path != "" {
		watcher.track(path, proc.Source)
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
			CWD: agent.CWD, Command: agent.Command, ProviderSessionID: environmentSessionID(agent),
		}
	}
	return processes, nil
}

// environmentSessionID is the session id an agent process carries in its own
// environment. Agents export their id to the commands they spawn, so a nested
// agent inherits its parent's: an id for a different source, or one stamped
// with another agent's CLAUDE_PID, belongs to an ancestor and is ignored.
func environmentSessionID(agent sessionprocess.Agent) string {
	source, sessionID := sessionprocess.IdentityFromEnvironment(agent.Environment)
	if source != agent.Source {
		return ""
	}
	if owner := agent.Environment["CLAUDE_PID"]; owner != "" && owner != strconv.Itoa(agent.PID) {
		return ""
	}
	return sessionID
}
