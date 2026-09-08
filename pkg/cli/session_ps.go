package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/cmux"
	sessionprocess "github.com/flanksource/captain/pkg/session/process"
	"github.com/flanksource/clicky/api"
	rpchttp "github.com/flanksource/clicky/rpc/http"
)

type PSOptions struct {
	// PIDs is []string rather than []int because clicky's CLI flag binder only
	// feeds positional args to string and []string fields — int and []int
	// fields silently bind their zero value (flags/assignment.go).
	PIDs    []string `args:"true" help:"Inspect these process IDs instead of scanning for agent sessions"`
	Source  string   `flag:"source" help:"Filter source: all, claude, codex" default:"all"`
	All     bool     `flag:"all" help:"Include agents from all projects" short:"a"`
	Project string   `flag:"project" help:"Restrict to an explicit project path"`
	Query   string   `flag:"q" help:"Search session id, cwd, pid, surface, or health"`
}

func (PSOptions) GetName() string { return "ps [pid...]" }

// psScopePID marks a result as a PID inspection rather than a scan, which is
// what switches the renderer from the table to per-process detail blocks.
const psScopePID = "pid"

type PSResult struct {
	Source   string  `json:"source" pretty:"label=Source"`
	Scope    string  `json:"scope" pretty:"label=Scope"`
	Project  string  `json:"project,omitempty" pretty:"label=Project"`
	Total    int     `json:"total" pretty:"label=Total"`
	Live     int     `json:"live" pretty:"label=Live"`
	Active   int     `json:"active" pretty:"label=Active"`
	Alerts   int     `json:"alerts,omitempty" pretty:"label=Alerts"`
	Tokens   string  `json:"tokens,omitempty" pretty:"label=Tokens"`
	Cost     string  `json:"cost,omitempty" pretty:"label=Cost"`
	Sessions []PSRow `json:"sessions"`
}

// RunPS lists currently-active agent sessions. Unlike `sessions live` (which
// starts from on-disk history and overlays live processes), `ps` starts from the
// live process listing, resolves each process's session-id/agent-ids/CMUX
// surface/last-activity from the OS (ps + lsof + environ), then augments each
// from the session cache/DB — so it reports only sessions with a live process.
//
// Given PIDs it inspects exactly those processes instead of scanning, and
// reports each one whether or not it is an agent: the caller named a process,
// so no source or project filter may exclude it, and a PID that is not running
// is an error rather than an empty result.
func RunPS(ctx context.Context, opts PSOptions) (PSResult, error) {
	pids, err := parsePSPIDs(opts.PIDs)
	if err != nil {
		return PSResult{}, err
	}
	source, err := normalizeSessionSource(opts.Source)
	if err != nil {
		return PSResult{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return PSResult{}, err
	}
	scope, projectRoot, _ := resolveSessionScope(cwd, opts.All, opts.Project)

	var processes []agentProcess
	if len(pids) > 0 {
		// An explicitly named PID outranks the scan's filters: the user asked
		// for that process, so neither its source nor its project can exclude it.
		scope, projectRoot = psScopePID, ""
		stopInspectPIDs := rpchttp.Track(ctx, "inspect-pids")
		processes, err = inspectSessionProcesses(ctx, pids)
		stopInspectPIDs()
	} else {
		stopDiscover := rpchttp.Track(ctx, "discover")
		processes, err = discoverSessionProcesses()
		stopDiscover()
	}
	if err != nil {
		return PSResult{}, err
	}
	if len(pids) == 0 {
		processes = filterProcessesBySource(processes, source)
		if projectRoot != "" {
			processes = filterAgentProcessesByProject(processes, projectRoot)
		}
	}

	stopInspect := rpchttp.Track(ctx, "inspect")
	enrichProcessesFromOS(processes)
	enrichSurfacesFromCmux(processes)
	stopInspect()

	stopAugment := rpchttp.Track(ctx, "augment")
	records := make([]SessionRecord, 0, len(processes))
	for i := range processes {
		record := psRecord(ctx, processes[i])
		if sessionMatchesQuery(record, opts.Query) {
			records = append(records, record)
		}
	}
	stopAugment()

	sortPSRecords(records)
	summary := summarizeSessionDashboard(records)
	rows := make([]PSRow, len(records))
	for i, record := range records {
		rows[i] = PSRow{record}
	}

	return PSResult{
		Source:   source,
		Scope:    scope,
		Project:  projectResultValue(scope, projectRoot),
		Total:    len(records),
		Live:     summary.LiveSessions,
		Active:   summary.ActiveSessions,
		Alerts:   summary.AlertSessions,
		Tokens:   psSummaryTokens(summary),
		Cost:     psSummaryCost(summary),
		Sessions: rows,
	}, nil
}

func psSummaryTokens(summary SessionDashboardWire) string {
	if summary.TotalTokens <= 0 {
		return ""
	}
	return api.HumanNumber(int64(summary.TotalTokens)).String()
}

func psSummaryCost(summary SessionDashboardWire) string {
	if summary.CostUSD <= 0 {
		return ""
	}
	return fmt.Sprintf("$%.2f", summary.CostUSD)
}

// parsePSPIDs turns the positional args into PIDs, rejecting anything that is
// not a usable process id rather than silently scanning instead.
func parsePSPIDs(args []string) ([]int, error) {
	pids := make([]int, 0, len(args))
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		pid, err := strconv.Atoi(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid PID %q", arg)
		}
		if pid <= 0 {
			return nil, fmt.Errorf("PID must be positive, got %d", pid)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func filterProcessesBySource(processes []agentProcess, source string) []agentProcess {
	if source == "all" {
		return processes
	}
	filtered := make([]agentProcess, 0, len(processes))
	for _, proc := range processes {
		if proc.Source == source {
			filtered = append(filtered, proc)
		}
	}
	return filtered
}

// Indirection points so RunPS can be tested without live processes.
var (
	discoverOpenSessionFiles = processOpenSessionFiles
	discoverCmuxSurfaces     = cmux.Surfaces
)

// enrichProcessesFromOS resolves each process's CMUX surface (from its
// environment) and session/agent identity + last activity (from the transcript
// files it holds open via a single lsof pass).
func enrichProcessesFromOS(processes []agentProcess) {
	openFiles := discoverOpenSessionFiles(processIDs(processes))
	for i := range processes {
		enrichProcessFromOpenFiles(&processes[i], openFiles[processes[i].PID])
	}
}

// enrichSurfacesFromCmux joins each process's CMUX surface id to cmux's own tree
// to attach the authoritative surface title and workspace name. Best-effort:
// when cmux is not running the processes keep their env-derived surface only.
func enrichSurfacesFromCmux(processes []agentProcess) {
	surfaces, err := discoverCmuxSurfaces()
	if err != nil || len(surfaces) == 0 {
		return
	}
	for i := range processes {
		s := processes[i].Surface
		if s == nil || s.SurfaceID == "" {
			continue
		}
		enrichCmuxSurface(s, surfaces)
	}
}

func enrichCmuxSurface(surface *CmuxSurface, surfaces map[string]cmux.Surface) {
	if info, ok := surfaces[surface.SurfaceID]; ok {
		surface.SurfaceRef = info.Ref
		surface.Title = info.Title
		surface.Workspace = info.Workspace
	}
}

type openTranscript struct {
	id   string
	kind string
	path string
	mod  time.Time
}

// enrichProcessFromOpenFiles fills SessionID/AgentIDs/SessionFile/LastActivity
// from the process's open transcripts. The primary session is the claude root
// (or, for codex, the most-recently-written rollout); every other open
// transcript of the same source is a sub-agent. Last activity is the newest
// write across all of them.
//
// Claude holds foreign transcript fds open (inherited across launches) and often
// does not hold its own current transcript open, so for claude the open set is
// restricted to the process's own project and, when that leaves nothing, the
// session is resolved by cwd instead (mirroring `sessions live`).
func enrichProcessFromOpenFiles(p *agentProcess, paths []string) {
	opens := collectOpenTranscripts(p.Source, paths)
	if p.Source == "claude" {
		resolveClaudeSession(p, filterClaudeOwnTranscripts(opens, p.CWD))
		return
	}
	if len(opens) == 0 {
		// Nothing open to attribute — a codex thread id or any other declared
		// identity in argv/env is all the process has to offer.
		p.SessionID = declaredSessionID(p)
		return
	}
	selectPrimaryTranscript(p, opens)
}

// declaredSessionID is the session id the launcher stamped on the process
// itself, via argv or the environment. Both are set explicitly by whatever
// started the agent, so they outrank transcript and cwd heuristics — notably
// for a session so new that no transcript has been written yet.
func declaredSessionID(p *agentProcess) string {
	if id := sessionprocess.SessionIDFromCommand(p.Command); id != "" {
		return id
	}
	_, id := sessionprocess.IdentityFromEnvironment(p.Environment)
	return id
}

// resolveClaudeSession sets the primary session by priority — (1) the id the
// launcher declared in argv or the environment, when its transcript exists,
// (2) the process's own-cwd open transcript, (3) the newest transcript under
// the cwd — then records any own-cwd sub-agent transcripts. The declared id
// leads because it is set explicitly by the launcher, whereas open fds are
// unreliable (claude inherits foreign transcript fds — the source of the
// original mis-attribution).
func resolveClaudeSession(p *agentProcess, ownOpens []openTranscript) {
	if id := declaredSessionID(p); id != "" {
		if path := locateClaudeTranscript(id, p.CWD); path != "" {
			setPrimaryFromFile(p, id, path)
		}
	}
	if p.SessionFile == "" && len(ownOpens) > 0 {
		idx, newest := selectPrimary(ownOpens)
		o := ownOpens[idx]
		p.SessionID, p.SessionFile = o.id, o.path
		la := newest
		p.LastActivity = &la
	}
	if p.SessionFile == "" {
		resolveClaudeSessionByCwd(p)
	}
	collectClaudeAgentIDs(p, ownOpens)
}

// locateClaudeTranscript finds the root transcript file for a session id,
// preferring one under the process's own cwd project, then any project.
func locateClaudeTranscript(id, cwd string) string {
	projectsDir := claude.GetProjectsDir()
	matches, err := filepath.Glob(filepath.Join(projectsDir, "*", id+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	if cwd != "" {
		suffix := strings.ToLower(claude.NormalizePath(cwd))
		for _, m := range matches {
			if strings.HasSuffix(strings.ToLower(claudeProjectName(m)), suffix) {
				return m
			}
		}
	}
	return matches[0]
}

func setPrimaryFromFile(p *agentProcess, id, path string) {
	p.SessionID = id
	p.SessionFile = path
	if info, err := os.Stat(path); err == nil {
		mod := info.ModTime()
		p.LastActivity = &mod
	}
}

// collectClaudeAgentIDs records every own-cwd open transcript that is not the
// primary session as a sub-agent.
func collectClaudeAgentIDs(p *agentProcess, ownOpens []openTranscript) {
	seen := map[string]bool{p.SessionID: true}
	for _, o := range ownOpens {
		if seen[o.id] {
			continue
		}
		seen[o.id] = true
		p.AgentIDs = append(p.AgentIDs, o.id)
	}
}

func collectOpenTranscripts(source string, paths []string) []openTranscript {
	var opens []openTranscript
	for _, path := range paths {
		src, id, kind := classifyOpenSessionFile(path)
		if src != source || id == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		opens = append(opens, openTranscript{id: id, kind: kind, path: path, mod: info.ModTime()})
	}
	return opens
}

// filterClaudeOwnTranscripts keeps only transcripts under the project directory
// matching the process's cwd, dropping foreign inherited fds.
func filterClaudeOwnTranscripts(opens []openTranscript, cwd string) []openTranscript {
	if cwd == "" {
		return opens
	}
	suffix := strings.ToLower(claude.NormalizePath(cwd))
	kept := opens[:0]
	for _, o := range opens {
		if strings.HasSuffix(strings.ToLower(claudeProjectName(o.path)), suffix) {
			kept = append(kept, o)
		}
	}
	return kept
}

// claudeProjectName returns the <project> directory segment of a claude
// transcript path (~/.claude/projects/<project>/...).
func claudeProjectName(path string) string {
	rel, err := filepath.Rel(claude.GetProjectsDir(), path)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return rel
}

// resolveClaudeSessionByCwd resolves a claude process that holds no own
// transcript open: it takes the id declared in argv/env, and only when there is
// none falls back to the newest transcript under the process's cwd project
// (like `sessions live`).
func resolveClaudeSessionByCwd(p *agentProcess) {
	// A declared id names this process's own session even when its transcript
	// does not exist yet; the newest transcript in the project is a different,
	// older session, so guessing it here would mis-attribute the process.
	if id := declaredSessionID(p); id != "" {
		p.SessionID = id
		return
	}
	files, err := claude.FindSessionFiles(claude.GetProjectsDir(), p.CWD, false)
	if err == nil {
		var newestPath string
		var newest time.Time
		for _, f := range files {
			info, err := os.Stat(f)
			if err != nil {
				continue
			}
			if newestPath == "" || info.ModTime().After(newest) {
				newestPath, newest = f, info.ModTime()
			}
		}
		if newestPath != "" {
			p.SessionID = sessionIDFromFile(newestPath)
			p.SessionFile = newestPath
			mod := newest
			p.LastActivity = &mod
		}
	}
}

// selectPrimaryTranscript picks the primary session among a process's open
// transcripts and records the rest as sub-agents.
func selectPrimaryTranscript(p *agentProcess, opens []openTranscript) {
	idx, newest := selectPrimary(opens)
	p.SessionID = opens[idx].id
	p.SessionFile = opens[idx].path
	activity := newest
	p.LastActivity = &activity
	collectClaudeAgentIDs(p, opens)
}

// selectPrimary returns the index of the primary transcript (claude root
// preferred, else most recent) and the newest write time across all of them.
func selectPrimary(opens []openTranscript) (int, time.Time) {
	primary := 0
	newest := opens[0].mod
	for i, o := range opens {
		if o.mod.After(newest) {
			newest = o.mod
		}
		if isPreferredPrimary(o, opens[primary]) {
			primary = i
		}
	}
	return primary, newest
}

// isPreferredPrimary reports whether candidate should replace current as the
// process's primary transcript: a claude root always wins over a sub-agent,
// otherwise the more-recently-written transcript wins.
func isPreferredPrimary(candidate, current openTranscript) bool {
	if candidate.kind == "root" && current.kind != "root" {
		return true
	}
	if candidate.kind != "root" && current.kind == "root" {
		return false
	}
	return candidate.mod.After(current.mod)
}

// psRecord builds the session record for a live process, augmenting it with the
// database summary (tokens, cost, context, model) when its session is known,
// and falling back to a minimal synthetic record otherwise.
func psRecord(ctx context.Context, proc agentProcess) SessionRecord {
	if proc.SessionID != "" {
		if db, err := captainDB(ctx); err == nil {
			if overview, err := db.GetSessionOverviewByIdentity(ctx, proc.SessionID); err == nil {
				record := recordFromOverview(*overview)
				applyLiveProcess(&record, proc)
				return record
			}
		}
	}
	record := SessionRecord{
		Key:       fmt.Sprintf("live-%s-%d", proc.Source, proc.PID),
		ID:        psRecordID(proc),
		Source:    proc.Source,
		CWD:       proc.CWD,
		StartedAt: proc.StartedAt,
	}
	applyLiveProcess(&record, proc)
	return record
}

func psRecordID(proc agentProcess) string {
	if proc.SessionID != "" {
		return proc.SessionID
	}
	return fmt.Sprintf("pid:%d", proc.PID)
}

func applyLiveProcess(record *SessionRecord, proc agentProcess) {
	record.Live = proc.wire()
	if record.CWD == "" {
		record.CWD = proc.CWD
	}
	if record.StartedAt == nil {
		record.StartedAt = proc.StartedAt
	}
	if record.EndedAt == nil {
		record.EndedAt = proc.LastActivity
	}
	record.Health = deriveSessionHealth(*record)
}

func sortPSRecords(records []SessionRecord) {
	sort.Slice(records, func(i, j int) bool {
		return psActivity(records[i]).After(psActivity(records[j]))
	})
}

func psActivity(record SessionRecord) time.Time {
	if record.Live != nil && record.Live.LastActivity != nil {
		return *record.Live.LastActivity
	}
	return sessionSortTime(record)
}
