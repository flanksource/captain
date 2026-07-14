package monitor

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
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
	if providerSessionID := parseClaudeSessionIDFromCommand(proc.Command); providerSessionID != "" {
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

// parseClaudeSessionIDFromCommand extracts the session id claude was launched
// with, from its "--session-id <uuid>" / "--resume <uuid>" argv (either the
// space-separated or "=<uuid>" form). Returns "" when absent.
func parseClaudeSessionIDFromCommand(command string) string {
	fields := strings.Fields(command)
	for i, field := range fields {
		for _, flag := range []string{"--session-id", "--resume"} {
			if field == flag && i+1 < len(fields) {
				return fields[i+1]
			}
			if value, ok := strings.CutPrefix(field, flag+"="); ok {
				return value
			}
		}
	}
	return ""
}

func discoverAgentProcesses() ([]Process, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	out, err := exec.Command("ps", "-eo", "pid=,pcpu=,pmem=,rss=,stat=,lstart=,command=").Output()
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(out, []byte{'\n'})
	processes := make([]Process, 0)
	for _, raw := range lines {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		proc, ok := parseAgentProcessLine(line)
		if !ok {
			continue
		}
		processes = append(processes, proc)
	}
	cwds := processCWDs(processIDs(processes))
	for i := range processes {
		processes[i].CWD = cwds[processes[i].PID]
	}
	return processes, nil
}

func parseAgentProcessLine(line string) (Process, bool) {
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return Process{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return Process{}, false
	}
	command := strings.Join(fields[10:], " ")
	source := processSource(command)
	if source == "" {
		return Process{}, false
	}
	cpu, _ := strconv.ParseFloat(fields[1], 64)
	mem, _ := strconv.ParseFloat(fields[2], 64)
	rss, _ := strconv.ParseInt(fields[3], 10, 64)
	stat := fields[4]
	start := parseProcessStart(strings.Join(fields[5:10], " "))
	status, _ := processStatus(stat)
	return Process{
		Source:        source,
		PID:           pid,
		Status:        status,
		CPUPercent:    cpu,
		MemoryPercent: mem,
		MemoryRSSKB:   rss,
		StartedAt:     start,
		Command:       command,
	}, true
}

func processSource(command string) string {
	lower := strings.ToLower(command)
	if strings.Contains(lower, "captain") || strings.Contains(lower, "ctop") || strings.Contains(lower, "claude-manager") {
		return ""
	}
	if strings.Contains(lower, "claude.app") {
		return ""
	}
	if commandNameMatches(lower, "claude") {
		return "claude"
	}
	if strings.Contains(lower, "codex-darwin") ||
		strings.Contains(lower, "codex-linux") ||
		strings.Contains(lower, "codex-win") ||
		commandNameMatches(lower, "codex") {
		// mcp-server / app-server are codex's tool/IPC servers, not interactive
		// sessions — they never hold a rollout transcript open.
		if commandNameMatches(lower, "mcp-server") || commandNameMatches(lower, "app-server") {
			return ""
		}
		return "codex"
	}
	return ""
}

func commandNameMatches(command, name string) bool {
	fields := strings.Fields(command)
	for _, field := range fields {
		base := field
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		base = strings.Trim(base, `"'`)
		if base == name {
			return true
		}
	}
	return false
}

func processStatus(stat string) (string, bool) {
	switch {
	case strings.Contains(stat, "Z"):
		return "zombie", false
	case strings.Contains(stat, "T"):
		return "stopped", false
	case strings.Contains(stat, "S"):
		return "sleeping", true
	default:
		return "active", true
	}
}

func parseProcessStart(value string) *time.Time {
	return parseProcessStartInLocation(value, time.Local)
}

func parseProcessStartInLocation(value string, location *time.Location) *time.Time {
	if value == "" {
		return nil
	}
	if location == nil {
		location = time.Local
	}
	// ps(1) renders lstart in the host's local timezone, but the value carries
	// no offset. time.Parse would interpret it as UTC and, on positive-offset
	// hosts, persist a process start several hours in the future. Closing that
	// row then violates ended_at >= process_started_at.
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", value, location)
	if err != nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func processIDs(processes []Process) []int {
	pids := make([]int, 0, len(processes))
	for _, proc := range processes {
		if proc.PID > 0 {
			pids = append(pids, proc.PID)
		}
	}
	return pids
}

func processCWDs(pids []int) map[int]string {
	cwds := make(map[int]string, len(pids))
	if runtime.GOOS == "linux" {
		for _, pid := range pids {
			if pid <= 0 {
				continue
			}
			cwd, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
			if err == nil {
				cwds[pid] = cwd
			}
		}
		return cwds
	}
	var pidList []string
	for _, pid := range pids {
		if pid > 0 {
			pidList = append(pidList, strconv.Itoa(pid))
		}
	}
	if len(pidList) == 0 {
		return cwds
	}
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-F", "pn", "-p", strings.Join(pidList, ",")).Output()
	if err != nil {
		return cwds
	}
	return parseLsofCWDs(out)
}

func parseLsofCWDs(out []byte) map[int]string {
	cwds := make(map[int]string)
	currentPID := 0
	for _, raw := range bytes.Split(out, []byte{'\n'}) {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, err := strconv.Atoi(strings.TrimPrefix(line, "p"))
			if err == nil {
				currentPID = pid
			}
		case 'n':
			if currentPID > 0 {
				cwds[currentPID] = strings.TrimPrefix(line, "n")
			}
		}
	}
	return cwds
}
