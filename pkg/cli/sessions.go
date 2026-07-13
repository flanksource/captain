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

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/google/uuid"
)

type SessionListOptions struct {
	Source  string `flag:"source" help:"Filter source: all, claude, codex" default:"all"`
	All     bool   `flag:"all" help:"Include sessions from all projects" short:"a"`
	Project string `flag:"project" help:"Restrict sessions to an explicit project path"`
	Query   string `flag:"q" help:"Search session id, model, cwd, branch, or provider"`
	Limit   int    `flag:"limit" help:"Maximum sessions to return; 0 means no limit" default:"100" short:"l"`
}

type SessionGetOptions struct {
	ID     string `flag:"id" args:"true" help:"Session id (full or unambiguous prefix)"`
	Offset int    `flag:"offset" help:"Skip this many messages from the start"`
	Limit  int    `flag:"limit" help:"Maximum messages to return; 0 means all" short:"l"`
	Tail   int    `flag:"tail" help:"Return only the last N messages (overrides offset/limit)"`
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

	scope, projectRoot, _ := resolveSessionScope(cwd, opts.All, opts.Project)
	db, err := freshenSessionDB(ctx)
	if err != nil {
		return SessionListResult{}, err
	}
	records, err := dbSessionRecords(ctx, db, sessionRecordQuery{
		Source: source, ProjectRoot: projectRoot, Query: opts.Query,
	})
	if err != nil {
		return SessionListResult{}, err
	}
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

// RunSessionGet returns the unified session model for a session id. Identity
// resolves against the database (full UUID or unambiguous provider-session-id
// prefix); the message stream is parsed read-only from the transcript at the
// DB-recorded path and paged via offset/limit/tail. Each message carries its
// transcript line (sourceLine) so consumers can seek into the raw file.
func RunSessionGet(ctx context.Context, opts SessionGetOptions) (*session.Session, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}
	db, err := freshenSessionDB(ctx)
	if err != nil {
		return nil, err
	}
	overview, err := resolveOverviewByAnyID(ctx, db, id)
	if err != nil {
		return nil, err
	}
	path := stringOr(overview.HistoryFile, stringOr(overview.Path, ""))
	if path == "" {
		return nil, fmt.Errorf("session %q has no transcript recorded on this host", id)
	}

	defer rpchttp.Track(ctx, "parse")()
	candidate := sessionCandidate{
		record: minimalSessionRecord(overview.Source, path, stringOr(overview.ProviderSessionID, overview.ID.String())),
		path:   path,
	}
	s, err := buildSessionModel(candidate)
	if err != nil {
		return nil, err
	}
	attachPromptRun(ctx, db, overview.ID, s)
	pageSessionMessages(s, opts)
	return s, nil
}

// pageSessionMessages windows the message stream: the last Tail messages, or
// an Offset/Limit slice from the start.
func pageSessionMessages(s *session.Session, opts SessionGetOptions) {
	if opts.Tail > 0 {
		if len(s.Messages) > opts.Tail {
			s.Messages = s.Messages[len(s.Messages)-opts.Tail:]
		}
		return
	}
	if opts.Offset <= 0 && opts.Limit <= 0 {
		return
	}
	offset := max(opts.Offset, 0)
	if offset >= len(s.Messages) {
		s.Messages = nil
		return
	}
	s.Messages = s.Messages[offset:]
	if opts.Limit > 0 && len(s.Messages) > opts.Limit {
		s.Messages = s.Messages[:opts.Limit]
	}
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

// attachPromptRun attaches the realized prompt (for captain-launched sessions)
// from the native prompt-run store to the session model.
func attachPromptRun(ctx context.Context, db *database.DB, sessionID uuid.UUID, s *session.Session) {
	runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{SessionID: &sessionID})
	if err != nil || len(runs) == 0 || len(runs[0].RenderedSpec) == 0 {
		return
	}
	if raw, err := json.Marshal(runs[0].RenderedSpec); err == nil {
		s.Prompt = raw
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

func minimalSessionRecord(source, file, id string) SessionRecord {
	return SessionRecord{
		Key:             sessionRecordKey(source, file),
		ID:              id,
		Source:          source,
		DetailAvailable: true,
	}
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
