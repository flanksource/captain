package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/session"
	rpchttp "github.com/flanksource/clicky/rpc/http"
)

type SessionListOptions struct {
	Source  string `flag:"source" help:"Filter source: all, claude, codex" default:"all"`
	All     bool   `flag:"all" help:"Include sessions from all projects" short:"a"`
	Project string `flag:"project" help:"Restrict sessions to an explicit project path"`
	Query   string `flag:"q" help:"Search session id, model, cwd, branch, or provider"`
	Limit   int    `flag:"limit" help:"Maximum sessions to return; 0 means no limit" default:"100" short:"l"`
}

type SessionGetOptions struct {
	ID     string `flag:"id" args:"true" help:"Session key or session id"`
	Source string `flag:"source" help:"Restrict source: all, claude, codex" default:"all"`
}

func (SessionGetOptions) GetName() string { return "get <id>" }

type SessionListResult struct {
	Sessions []SessionRecord `json:"sessions"`
	Total    int             `json:"total"`
	Source   string          `json:"source"`
	Scope    string          `json:"scope"`
	Project  string          `json:"project,omitempty"`
}

type SessionLiveOptions struct {
	Source  string `flag:"source" help:"Filter source: all, claude, codex" default:"all"`
	All     bool   `flag:"all" help:"Include sessions from all projects" short:"a"`
	Project string `flag:"project" help:"Restrict sessions to an explicit project path"`
	Query   string `flag:"q" help:"Search session id, model, cwd, branch, provider, pid, or health"`
	Limit   int    `flag:"limit" help:"Maximum sessions to return" default:"25" short:"l"`
	Full    bool   `flag:"full" help:"Parse all matching history exactly; ignores --limit"`
}

type SessionLiveResult struct {
	Sessions []SessionRecord      `json:"sessions"`
	Total    int                  `json:"total"`
	Source   string               `json:"source"`
	Scope    string               `json:"scope"`
	Project  string               `json:"project,omitempty"`
	Summary  SessionDashboardWire `json:"summary"`
}

type SessionRecord struct {
	Key             string              `json:"key"`
	ID              string              `json:"id"`
	Source          string              `json:"source"`
	Project         string              `json:"project,omitempty"`
	Slug            string              `json:"slug,omitempty"`
	Title           string              `json:"title,omitempty"`
	InitialPrompt   string              `json:"initialPrompt,omitempty"`
	StartedAt       *time.Time          `json:"startedAt,omitempty"`
	EndedAt         *time.Time          `json:"endedAt,omitempty"`
	Model           string              `json:"model,omitempty"`
	ReasoningEffort string              `json:"reasoningEffort,omitempty"`
	Version         string              `json:"version,omitempty"`
	GitBranch       string              `json:"gitBranch,omitempty"`
	Provider        string              `json:"provider,omitempty"`
	CWD             string              `json:"cwd,omitempty"`
	ToolCalls       int                 `json:"toolCalls"`
	Messages        int                 `json:"messages"`
	DetailAvailable bool                `json:"detailAvailable"`
	Tokens          *SessionTokensWire  `json:"tokens,omitempty"`
	Context         *SessionContextWire `json:"context,omitempty"`
	CostUSD         float64             `json:"costUsd,omitempty"`
	Live            *SessionLiveWire    `json:"live,omitempty"`
	Health          []SessionHealthWire `json:"health,omitempty"`
}

type SessionTokensWire struct {
	InputTokens         int `json:"inputTokens,omitempty"`
	OutputTokens        int `json:"outputTokens,omitempty"`
	CacheReadTokens     int `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int `json:"cacheCreationTokens,omitempty"`
	TotalTokens         int `json:"totalTokens,omitempty"`
}

type SessionContextWire struct {
	UsedTokens   int `json:"usedTokens,omitempty"`
	WindowTokens int `json:"windowTokens,omitempty"`
	FreePercent  int `json:"freePercent"`
}

type SessionLiveWire struct {
	PID           int          `json:"pid,omitempty"`
	Status        string       `json:"status,omitempty"`
	Active        bool         `json:"active"`
	CPUPercent    float64      `json:"cpuPercent,omitempty"`
	MemoryPercent float64      `json:"memoryPercent,omitempty"`
	StartedAt     *time.Time   `json:"startedAt,omitempty"`
	CWD           string       `json:"cwd,omitempty"`
	Command       string       `json:"command,omitempty"`
	SessionID     string       `json:"sessionId,omitempty"`
	AgentIDs      []string     `json:"agentIds,omitempty"`
	LastActivity  *time.Time   `json:"lastActivity,omitempty"`
	SessionFile   string       `json:"sessionFile,omitempty"`
	Surface       *CmuxSurface `json:"surface,omitempty"`
}

// CmuxSurface identifies the cmux multiplexer surface hosting an agent process.
// SurfaceID/WorkspaceID/… are derived from the process's CMUX_* environment
// variables; Title/Workspace are the authoritative names joined from cmux itself.
type CmuxSurface struct {
	SurfaceID   string `json:"surfaceId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	TabID       string `json:"tabId,omitempty"`
	PanelID     string `json:"panelId,omitempty"`
	Port        int    `json:"port,omitempty"`
	AgentKind   string `json:"agentKind,omitempty"`
	SocketPath  string `json:"socketPath,omitempty"`
	ClaudePID   int    `json:"claudePid,omitempty"`
	Title       string `json:"title,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
}

type SessionHealthWire struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type SessionDashboardWire struct {
	TotalSessions       int     `json:"totalSessions"`
	LiveSessions        int     `json:"liveSessions"`
	ActiveSessions      int     `json:"activeSessions"`
	StoppedSessions     int     `json:"stoppedSessions"`
	AlertSessions       int     `json:"alertSessions"`
	InputTokens         int     `json:"inputTokens,omitempty"`
	OutputTokens        int     `json:"outputTokens,omitempty"`
	CacheReadTokens     int     `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int     `json:"cacheCreationTokens,omitempty"`
	TotalTokens         int     `json:"totalTokens,omitempty"`
	CostUSD             float64 `json:"costUsd,omitempty"`
	LowestContextFree   *int    `json:"lowestContextFree,omitempty"`
}

func (t SessionTokensWire) String() string {
	if t.TotalTokens > 0 {
		return compactSessionInt(t.TotalTokens)
	}
	total := t.InputTokens + t.OutputTokens + t.CacheReadTokens + t.CacheCreationTokens
	if total > 0 {
		return compactSessionInt(total)
	}
	return ""
}

func (c SessionContextWire) String() string {
	if c.WindowTokens > 0 {
		return fmt.Sprintf("%d%% free", c.FreePercent)
	}
	if c.FreePercent > 0 {
		return fmt.Sprintf("%d%% free", c.FreePercent)
	}
	return ""
}

func (l SessionLiveWire) String() string {
	if l.PID == 0 && l.Status == "" && l.Command == "" {
		return ""
	}
	parts := make([]string, 0, 6)
	if l.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid %d", l.PID))
	}
	if l.Status != "" {
		parts = append(parts, l.Status)
	}
	if l.CPUPercent > 0 {
		parts = append(parts, fmt.Sprintf("%.1f%% cpu", l.CPUPercent))
	}
	if l.MemoryPercent > 0 {
		parts = append(parts, fmt.Sprintf("%.1f%% mem", l.MemoryPercent))
	}
	if len(l.AgentIDs) > 0 {
		parts = append(parts, fmt.Sprintf("%d agents", len(l.AgentIDs)))
	}
	if l.Surface != nil && l.Surface.SurfaceID != "" {
		parts = append(parts, "cmux:"+shortSessionID(l.Surface.SurfaceID))
	}
	if command := compactSessionCommand(l.Command); command != "" {
		parts = append(parts, command)
	}
	return strings.Join(parts, " ")
}

func (h SessionHealthWire) String() string {
	if h.Kind == "" {
		return h.Message
	}
	if h.Severity == "" {
		return h.Kind
	}
	return h.Severity + ":" + h.Kind
}

func compactSessionCommand(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	base := filepath.Base(strings.Trim(fields[0], `"'`))
	if len(fields) > 1 {
		return base + " ..."
	}
	return base
}

func compactSessionInt(value int) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%dk", value/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

type sessionCandidate struct {
	record SessionRecord
	path   string
}

func RunSessionList(ctx context.Context, opts SessionListOptions) (SessionListResult, error) {
	source, err := normalizeSessionSource(opts.Source)
	if err != nil {
		return SessionListResult{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return SessionListResult{}, err
	}

	scope, projectRoot, searchAll := resolveSessionScope(cwd, opts.All, opts.Project)
	candidates, err := discoverSessionCandidates(ctx, cwd, searchAll, source)
	if err != nil {
		return SessionListResult{}, err
	}
	candidates = filterSessionCandidatesByProject(candidates, projectRoot)
	records := make([]SessionRecord, 0, len(candidates))
	for _, candidate := range candidates {
		if sessionMatchesQuery(candidate.record, opts.Query) {
			records = append(records, candidate.record)
		}
	}
	sortSessionRecords(records)
	total := len(records)
	if opts.Limit > 0 && len(records) > opts.Limit {
		records = records[:opts.Limit]
	}

	return SessionListResult{
		Sessions: records,
		Total:    total,
		Source:   source,
		Scope:    scope,
		Project:  projectResultValue(scope, projectRoot),
	}, nil
}

// RunSessionGet returns the unified session model for a session id. The viewer
// consumes this model natively — one shape for both Claude and Codex, carrying
// the agent hierarchy, cost, changed files, plan events, approvals, and the
// message stream (with per-message provenance and the raw JSONL line).
func RunSessionGet(ctx context.Context, opts SessionGetOptions) (*session.Session, error) {
	source, err := normalizeSessionSource(opts.Source)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}

	stopFind := rpchttp.Track(ctx, "find")
	candidate, found, err := findSessionCandidateByID(id, source)
	stopFind()
	if err != nil {
		return nil, err
	} else if found {
		return buildUnifiedSession(ctx, candidate)
	}

	candidates, err := discoverSessionCandidates(ctx, "", true, source)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.record.Key != id && candidate.record.ID != id && !strings.HasPrefix(candidate.record.ID, id) {
			continue
		}
		return buildUnifiedSession(ctx, candidate)
	}
	return nil, fmt.Errorf("session %q not found", id)
}

// buildUnifiedSession builds the pkg/session model for a resolved candidate: a
// Claude session (root + sub-agent transcripts, resolved by id) or a Codex
// session file.
func buildUnifiedSession(ctx context.Context, candidate sessionCandidate) (*session.Session, error) {
	defer rpchttp.Track(ctx, "parse")()
	s, err := buildSessionModel(candidate)
	if err != nil {
		return nil, err
	}
	persistSessionRows(candidate, s)
	attachStoredPrompt(s)
	return s, nil
}

func buildSessionModel(candidate sessionCandidate) (*session.Session, error) {
	switch candidate.record.Source {
	case "claude":
		id := candidate.record.ID
		if id == "" {
			id = sessionIDFromFile(candidate.path)
		}
		sessions, err := session.Build("", true, claude.Filter{
			SessionIDs:    []string{id},
			KeepRaw:       true,
			IncludeAgents: true,
		})
		if err != nil {
			return nil, err
		}
		for _, s := range sessions {
			if s.ID == id {
				return s, nil
			}
		}
		if len(sessions) > 0 {
			return sessions[0], nil
		}
		return nil, fmt.Errorf("session %q not found", id)
	case "codex":
		sessions := session.BuildCodex([]string{candidate.path})
		if len(sessions) == 0 {
			return nil, fmt.Errorf("codex session %q not parseable", candidate.path)
		}
		return sessions[0], nil
	default:
		return nil, fmt.Errorf("unknown session source %q", candidate.record.Source)
	}
}

// persistSessionRows upserts the session's rows (root + sub-agents linked by
// parent) so the store's hierarchy is populated on a detailed read.
func persistSessionRows(candidate sessionCandidate, s *session.Session) {
	st := sessionStore()
	if st == nil {
		return
	}
	switch candidate.record.Source {
	case "claude":
		parsed, err := claude.ParseSessions("", true, claude.Filter{SessionIDs: []string{s.ID}, IncludeAgents: true})
		if err != nil {
			return
		}
		for _, ps := range parsed {
			if ps.SessionID == s.ID {
				st.upsertRows(session.Rows(ps))
			}
		}
	case "codex":
		if r, ok := session.CodexRow(candidate.path); ok {
			st.upsertRows([]session.Row{r})
		}
	}
}

// attachStoredPrompt attaches the realized prompt (for captain-launched
// sessions) from the store to the session model.
func attachStoredPrompt(s *session.Session) {
	st := sessionStore()
	if st == nil || s.ID == "" {
		return
	}
	if p, ok := st.prompt(s.ID); ok {
		if raw, err := json.Marshal(p.Realized); err == nil {
			s.Prompt = raw
		}
	}
}

func normalizeSessionSource(source string) (string, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	switch source {
	case "", "all":
		return "all", nil
	case "claude", "codex":
		return source, nil
	default:
		return "", fmt.Errorf("invalid source %q: expected all, claude, or codex", source)
	}
}

func normalizeSessionProject(project string) string {
	project = strings.TrimSpace(project)
	if project == "" || strings.EqualFold(project, "all") {
		return ""
	}
	if abs, err := filepath.Abs(project); err == nil {
		project = abs
	}
	return filepath.Clean(project)
}

func resolveSessionScope(cwd string, all bool, project string) (scope string, projectRoot string, searchAll bool) {
	if project = normalizeSessionProject(project); project != "" {
		return "project", sessionProjectRoot(project), true
	}
	if all {
		return "all", "", true
	}
	return "current", sessionProjectRoot(cwd), false
}

func projectResultValue(scope, projectRoot string) string {
	if scope == "project" {
		return projectRoot
	}
	return ""
}

func filterSessionCandidatesByProject(candidates []sessionCandidate, projectRoot string) []sessionCandidate {
	if projectRoot == "" {
		return candidates
	}
	filtered := make([]sessionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if sessionRecordMatchesProject(candidate.record, projectRoot) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func discoverSessionCandidates(ctx context.Context, cwd string, searchAll bool, source string) ([]sessionCandidate, error) {
	var candidates []sessionCandidate
	if source == "all" || source == "claude" {
		claudeSessions, err := discoverClaudeSessions(ctx, cwd, searchAll)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, claudeSessions...)
	}
	if source == "all" || source == "codex" {
		codexSessions, err := discoverCodexSessions(ctx, cwd, searchAll)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, codexSessions...)
	}
	return candidates, nil
}

func findSessionCandidateByID(id string, source string) (sessionCandidate, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return sessionCandidate{}, false, nil
	}
	if source == "all" || source == "claude" {
		candidate, ok, err := findClaudeSessionCandidateByID(id)
		if err != nil || ok {
			return candidate, ok, err
		}
	}
	if source == "all" || source == "codex" {
		candidate, ok, err := findCodexSessionCandidateByID(id)
		if err != nil || ok {
			return candidate, ok, err
		}
	}
	return sessionCandidate{}, false, nil
}

func findClaudeSessionCandidateByID(id string) (sessionCandidate, bool, error) {
	if hasPathOrGlobMeta(id) {
		return sessionCandidate{}, false, nil
	}
	projectsDir := claude.GetProjectsDir()
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return sessionCandidate{}, false, nil
	} else if err != nil {
		return sessionCandidate{}, false, err
	}

	files, err := filepath.Glob(filepath.Join(projectsDir, "*", id+"*.jsonl"))
	if err != nil {
		return sessionCandidate{}, false, err
	}
	sort.Strings(files)
	for _, file := range files {
		sessionID := sessionIDFromFile(file)
		if sessionID != id && !strings.HasPrefix(sessionID, id) {
			continue
		}
		return sessionCandidate{
			record: minimalSessionRecord("claude", file, sessionID),
			path:   file,
		}, true, nil
	}
	return sessionCandidate{}, false, nil
}

func findCodexSessionCandidateByID(id string) (sessionCandidate, bool, error) {
	files, err := history.FindCodexSessionFiles()
	if err != nil {
		return sessionCandidate{}, false, err
	}
	sort.Strings(files)
	for _, file := range files {
		key := sessionRecordKey("codex", file)
		fileID := sessionIDFromFile(file)
		if key == id || fileID == id || (fileID != "" && strings.HasPrefix(fileID, id)) {
			return sessionCandidate{
				record: minimalSessionRecord("codex", file, fileID),
				path:   file,
			}, true, nil
		}
		meta, err := history.ReadCodexSessionMeta(file)
		if err != nil || meta == nil || meta.ID == "" {
			continue
		}
		if meta.ID == id || strings.HasPrefix(meta.ID, id) {
			record := minimalSessionRecord("codex", file, meta.ID)
			record.CWD = meta.CWD
			record.Provider = meta.ModelProvider
			record.Version = meta.CLIVersion
			record.GitBranch = meta.GitBranch
			record.StartedAt = meta.StartedAt
			return sessionCandidate{record: record, path: file}, true, nil
		}
	}
	return sessionCandidate{}, false, nil
}

func minimalSessionRecord(source, file, id string) SessionRecord {
	return SessionRecord{
		Key:             sessionRecordKey(source, file),
		ID:              id,
		Source:          source,
		DetailAvailable: true,
	}
}

func hasPathOrGlobMeta(s string) bool {
	return strings.ContainsAny(s, `/\*?[`)
}

func discoverClaudeSessions(ctx context.Context, cwd string, searchAll bool) ([]sessionCandidate, error) {
	stopFind := rpchttp.Track(ctx, "find")
	files, err := claude.FindSessionFiles(claude.GetProjectsDir(), cwd, searchAll)
	stopFind()
	if err != nil {
		return nil, err
	}
	refs := make([]sessionFileRef, len(files))
	for i, file := range files {
		refs[i] = sessionFileRef{source: "claude", path: file}
	}
	return summarizeSessionRefs(ctx, refs), nil
}

func discoverCodexSessions(ctx context.Context, cwd string, searchAll bool) ([]sessionCandidate, error) {
	stopFind := rpchttp.Track(ctx, "find")
	files, err := history.FindCodexSessionFiles()
	if err != nil {
		stopFind()
		return nil, err
	}
	matchRoot := cwd
	if cwd != "" {
		projectInfo := claude.FindProjectInfo(cwd)
		if projectInfo.Root != "" {
			matchRoot = projectInfo.Root
		}
	}
	refs := make([]sessionFileRef, 0, len(files))
	for _, file := range files {
		if !searchAll {
			meta, err := history.ReadCodexSessionMeta(file)
			if err != nil || meta == nil || !codexMetaMatchesProject(meta, matchRoot) {
				continue
			}
		}
		refs = append(refs, sessionFileRef{source: "codex", path: file})
	}
	stopFind()
	return summarizeSessionRefs(ctx, refs), nil
}

func codexMetaMatchesProject(meta *history.CodexSessionInfo, projectRoot string) bool {
	if meta == nil {
		return false
	}
	return sessionRecordMatchesProject(SessionRecord{CWD: meta.CWD}, projectRoot)
}

func sessionRecordKey(source, path string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + path))
	return source + "-" + hex.EncodeToString(sum[:])[:16]
}

func sessionMatchesQuery(record SessionRecord, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	values := []string{
		record.Key,
		record.ID,
		record.Source,
		record.Project,
		record.Title,
		record.InitialPrompt,
		record.Model,
		record.ReasoningEffort,
		record.Version,
		record.GitBranch,
		record.Provider,
		record.CWD,
		filepath.Base(record.CWD),
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	if liveMatchesQuery(record.Live, query) || healthMatchesQuery(record.Health, query) {
		return true
	}
	return false
}

func sortSessionRecords(records []SessionRecord) {
	sort.Slice(records, func(i, j int) bool {
		return sessionSortTime(records[i]).After(sessionSortTime(records[j]))
	})
}

func sessionSortTime(record SessionRecord) time.Time {
	if record.EndedAt != nil {
		return *record.EndedAt
	}
	if record.StartedAt != nil {
		return *record.StartedAt
	}
	return time.Time{}
}
