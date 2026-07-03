package cli

import (
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
)

type SessionListOptions struct {
	Source string `flag:"source" help:"Filter source: all, claude, codex" default:"all"`
	All    bool   `flag:"all" help:"Include sessions from all projects" short:"a"`
	Query  string `flag:"q" help:"Search session id, model, cwd, branch, or provider"`
	Limit  int    `flag:"limit" help:"Maximum sessions to return; 0 means no limit" default:"100" short:"l"`
}

type SessionGetOptions struct {
	ID     string `flag:"id" args:"true" help:"Session key or session id" required:"true"`
	Source string `flag:"source" help:"Restrict source: all, claude, codex" default:"all"`
}

func (SessionGetOptions) GetName() string { return "get <id>" }

type SessionListResult struct {
	Sessions []SessionRecord `json:"sessions"`
	Total    int             `json:"total"`
	Source   string          `json:"source"`
	Scope    string          `json:"scope"`
}

type SessionLiveOptions struct {
	Source string `flag:"source" help:"Filter source: all, claude, codex" default:"all"`
	All    bool   `flag:"all" help:"Include sessions from all projects" short:"a"`
	Query  string `flag:"q" help:"Search session id, model, cwd, branch, provider, pid, or health"`
	Limit  int    `flag:"limit" help:"Maximum sessions to return" default:"25" short:"l"`
	Full   bool   `flag:"full" help:"Parse all matching history exactly; ignores --limit"`
}

type SessionLiveResult struct {
	Sessions []SessionRecord      `json:"sessions"`
	Total    int                  `json:"total"`
	Source   string               `json:"source"`
	Scope    string               `json:"scope"`
	Summary  SessionDashboardWire `json:"summary"`
}

type SessionRecord struct {
	Key             string              `json:"key"`
	ID              string              `json:"id"`
	Source          string              `json:"source"`
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
	Entries         []SessionEntryWire  `json:"entries,omitempty"`
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
	PID           int        `json:"pid,omitempty"`
	Status        string     `json:"status,omitempty"`
	Active        bool       `json:"active"`
	CPUPercent    float64    `json:"cpuPercent,omitempty"`
	MemoryPercent float64    `json:"memoryPercent,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	CWD           string     `json:"cwd,omitempty"`
	Command       string     `json:"command,omitempty"`
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
	parts := make([]string, 0, 4)
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

type SessionEntryWire struct {
	Type              string              `json:"type,omitempty"`
	ToolUse           *SessionToolUseWire `json:"tool_use,omitempty"`
	Message           *SessionMessageWire `json:"message,omitempty"`
	Timestamp         string              `json:"timestamp,omitempty"`
	CWD               string              `json:"cwd,omitempty"`
	SessionID         string              `json:"sessionId,omitempty"`
	UUID              string              `json:"uuid,omitempty"`
	IsAPIErrorMessage bool                `json:"isApiErrorMessage,omitempty"`
	APIErrorStatus    int                 `json:"apiErrorStatus,omitempty"`
	Error             string              `json:"error,omitempty"`
}

type SessionMessageWire struct {
	Role       string               `json:"role,omitempty"`
	StopReason string               `json:"stop_reason,omitempty"`
	Content    []SessionContentWire `json:"content,omitempty"`
}

type SessionContentWire struct {
	Type     string         `json:"type,omitempty"`
	Text     string         `json:"text,omitempty"`
	Thinking string         `json:"thinking,omitempty"`
	Name     string         `json:"name,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
	ID       string         `json:"id,omitempty"`
}

type SessionToolUseWire struct {
	Tool            string         `json:"tool,omitempty"`
	Input           map[string]any `json:"input,omitempty"`
	Timestamp       string         `json:"timestamp,omitempty"`
	CWD             string         `json:"cwd,omitempty"`
	SessionID       string         `json:"session_id,omitempty"`
	ToolUseID       string         `json:"tool_use_id,omitempty"`
	Source          string         `json:"source,omitempty"`
	Model           string         `json:"model,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Response        string         `json:"response,omitempty"`
}

type sessionCandidate struct {
	record SessionRecord
	path   string
}

func RunSessionList(opts SessionListOptions) (SessionListResult, error) {
	source, err := normalizeSessionSource(opts.Source)
	if err != nil {
		return SessionListResult{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return SessionListResult{}, err
	}

	candidates, err := discoverSessionCandidates(cwd, opts.All, source)
	if err != nil {
		return SessionListResult{}, err
	}
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

	scope := "current"
	if opts.All {
		scope = "all"
	}
	return SessionListResult{
		Sessions: records,
		Total:    total,
		Source:   source,
		Scope:    scope,
	}, nil
}

func RunSessionGet(opts SessionGetOptions) (SessionRecord, error) {
	source, err := normalizeSessionSource(opts.Source)
	if err != nil {
		return SessionRecord{}, err
	}
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		return SessionRecord{}, fmt.Errorf("id is required")
	}

	if candidate, ok, err := findSessionCandidateByID(id, source); err != nil {
		return SessionRecord{}, err
	} else if ok {
		return loadSessionDetail(candidate)
	}

	candidates, err := discoverSessionCandidates("", true, source)
	if err != nil {
		return SessionRecord{}, err
	}
	for _, candidate := range candidates {
		if candidate.record.Key != id && candidate.record.ID != id && !strings.HasPrefix(candidate.record.ID, id) {
			continue
		}
		record, err := loadSessionDetail(candidate)
		if err != nil {
			return SessionRecord{}, err
		}
		return record, nil
	}
	return SessionRecord{}, fmt.Errorf("session %q not found", id)
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

func discoverSessionCandidates(cwd string, searchAll bool, source string) ([]sessionCandidate, error) {
	var candidates []sessionCandidate
	if source == "all" || source == "claude" {
		claudeSessions, err := discoverClaudeSessions(cwd, searchAll)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, claudeSessions...)
	}
	if source == "all" || source == "codex" {
		codexSessions, err := discoverCodexSessions(cwd, searchAll)
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

func discoverClaudeSessions(cwd string, searchAll bool) ([]sessionCandidate, error) {
	files, err := claude.FindSessionFiles(claude.GetProjectsDir(), cwd, searchAll)
	if err != nil {
		return nil, err
	}
	candidates := make([]sessionCandidate, 0, len(files))
	for _, file := range files {
		record, err := summarizeClaudeSessionFile(file)
		if err != nil {
			continue
		}
		candidates = append(candidates, sessionCandidate{record: record, path: file})
	}
	return candidates, nil
}

func discoverCodexSessions(cwd string, searchAll bool) ([]sessionCandidate, error) {
	files, err := history.FindCodexSessionFiles()
	if err != nil {
		return nil, err
	}
	matchRoot := cwd
	if cwd != "" {
		projectInfo := claude.FindProjectInfo(cwd)
		if projectInfo.Root != "" {
			matchRoot = projectInfo.Root
		}
	}

	candidates := make([]sessionCandidate, 0, len(files))
	for _, file := range files {
		if !searchAll {
			meta, err := history.ReadCodexSessionMeta(file)
			if err != nil || meta == nil || !codexMetaMatchesProject(meta, matchRoot) {
				continue
			}
		}
		record, err := summarizeCodexSessionFile(file)
		if err != nil || record.ID == "" {
			continue
		}
		candidates = append(candidates, sessionCandidate{record: record, path: file})
	}
	return candidates, nil
}

func codexMetaMatchesProject(meta *history.CodexSessionInfo, projectRoot string) bool {
	if meta == nil {
		return false
	}
	return sessionRecordMatchesProject(SessionRecord{CWD: meta.CWD}, projectRoot)
}

func summarizeClaudeSession(file string, entries []claude.HistoryEntry) SessionRecord {
	record := SessionRecord{
		Key:             sessionRecordKey("claude", file),
		ID:              sessionIDFromFile(file),
		Source:          "claude",
		DetailAvailable: true,
	}
	for _, entry := range entries {
		ts, err := entry.ParseTimestamp()
		if err == nil {
			extendSessionRange(&record, ts)
		}
		if entry.SessionID != "" {
			record.ID = entry.SessionID
		}
		if record.Model == "" && entry.Message.Model != "" {
			record.Model = entry.Message.Model
		}
		if record.Version == "" && entry.Version != "" {
			record.Version = entry.Version
		}
		if record.GitBranch == "" && entry.GitBranch != "" {
			record.GitBranch = entry.GitBranch
		}
		if record.CWD == "" && entry.CWD != "" {
			record.CWD = entry.CWD
		}
		if entry.Message.Role == claude.MessageRoleUser || entry.Message.Role == claude.MessageRoleAssistant {
			if len(messageTextBlocks(entry)) > 0 {
				record.Messages++
			}
		}
		record.ToolCalls += len(entry.Message.GetToolUses())
	}
	return record
}

func summarizeCodexSession(file string, uses []history.ToolUse) SessionRecord {
	record := SessionRecord{
		Key:             sessionRecordKey("codex", file),
		Source:          "codex",
		DetailAvailable: true,
	}
	for _, use := range uses {
		if use.SessionID != "" {
			record.ID = use.SessionID
		}
		if use.Timestamp != nil {
			extendSessionRange(&record, *use.Timestamp)
		}
		if record.CWD == "" && use.CWD != "" {
			record.CWD = use.CWD
		}
		if record.Model == "" && use.Model != "" {
			record.Model = use.Model
		}
		if record.ReasoningEffort == "" && use.ReasoningEffort != "" {
			record.ReasoningEffort = use.ReasoningEffort
		}
		if use.Tool == "Assistant" || use.Tool == "Reasoning" {
			record.Messages++
		} else {
			record.ToolCalls++
		}
	}
	if meta, err := history.ReadCodexSessionInfo(file); err == nil && meta != nil {
		if record.ID == "" {
			record.ID = meta.ID
		}
		if record.CWD == "" {
			record.CWD = meta.CWD
		}
		record.Provider = meta.ModelProvider
		record.Version = meta.CLIVersion
		record.GitBranch = meta.GitBranch
		if record.Model == "" {
			record.Model = meta.Model
		}
		if record.ReasoningEffort == "" {
			record.ReasoningEffort = meta.ReasoningEffort
		}
		if record.StartedAt == nil && meta.StartedAt != nil {
			record.StartedAt = meta.StartedAt
		}
	}
	if record.ID == "" {
		record.ID = sessionIDFromFile(file)
	}
	return record
}

func loadSessionDetail(candidate sessionCandidate) (SessionRecord, error) {
	record := candidate.record
	switch record.Source {
	case "claude":
		entries, err := claude.ReadHistoryFile(candidate.path)
		if err != nil {
			return SessionRecord{}, err
		}
		record = summarizeClaudeSession(candidate.path, entries)
		record.Entries = claudeEntriesForViewer(entries)
	case "codex":
		uses, err := history.ExtractCodexToolUses(candidate.path)
		if err != nil {
			return SessionRecord{}, err
		}
		record = summarizeCodexSession(candidate.path, uses)
		record.Entries = codexEntriesForViewer(uses)
	default:
		return SessionRecord{}, fmt.Errorf("unknown session source %q", record.Source)
	}
	return record, nil
}

func claudeEntriesForViewer(entries []claude.HistoryEntry) []SessionEntryWire {
	toolUses := claude.ExtractToolUsesWithTokens(entries)
	toolByID := make(map[string]claude.ToolUse, len(toolUses))
	for _, use := range toolUses {
		if use.ToolUseID != "" {
			toolByID[use.ToolUseID] = use
		}
	}

	var out []SessionEntryWire
	for entryIndex, entry := range entries {
		if blocks := messageTextBlocks(entry); len(blocks) > 0 {
			out = append(out, SessionEntryWire{
				Type:      string(entry.Message.Role),
				Message:   &SessionMessageWire{Role: string(entry.Message.Role), StopReason: string(entry.Message.StopReason), Content: blocks},
				Timestamp: entry.Timestamp,
				CWD:       entry.CWD,
				SessionID: entry.SessionID,
				UUID:      entry.UUID,
			})
		}

		for blockIndex, block := range entry.Message.Content {
			if block.Type != claude.ContentTypeToolUse {
				continue
			}
			use, ok := toolByID[block.ID]
			if !ok {
				use = claude.ToolUse{
					Tool:      block.Name,
					Input:     rawJSONMap(block.Input),
					SessionID: entry.SessionID,
					ToolUseID: block.ID,
					Source:    "claude",
				}
				if ts, err := entry.ParseTimestamp(); err == nil {
					use.Timestamp = &ts
				}
			}
			if use.Source == "" {
				use.Source = "claude"
			}
			if use.Model == "" {
				use.Model = entry.Message.Model
			}
			if use.CWD == "" {
				use.CWD = entry.CWD
			}
			out = append(out, SessionEntryWire{
				Type:      "assistant",
				ToolUse:   claudeToolUseForViewer(use),
				Timestamp: entry.Timestamp,
				CWD:       entry.CWD,
				SessionID: entry.SessionID,
				UUID:      fallbackUUID(entry.UUID, entryIndex, blockIndex),
			})
		}
	}
	return out
}

func codexEntriesForViewer(uses []history.ToolUse) []SessionEntryWire {
	out := make([]SessionEntryWire, 0, len(uses))
	for i, use := range uses {
		out = append(out, SessionEntryWire{
			Type:      "assistant",
			ToolUse:   codexToolUseForViewer(use),
			Timestamp: formatOptionalTime(use.Timestamp),
			CWD:       use.CWD,
			SessionID: use.SessionID,
			UUID:      fmt.Sprintf("codex-%d", i),
		})
	}
	return out
}

func messageTextBlocks(entry claude.HistoryEntry) []SessionContentWire {
	var blocks []SessionContentWire
	for _, block := range entry.Message.Content {
		switch block.Type {
		case claude.ContentTypeText:
			if block.Text != "" {
				blocks = append(blocks, SessionContentWire{Type: "text", Text: block.Text, ID: block.ID})
			}
		case claude.ContentTypeThinking, claude.ContentTypeRedactedThinking:
			if block.Thinking != "" {
				blocks = append(blocks, SessionContentWire{Type: "thinking", Thinking: block.Thinking, ID: block.ID})
			}
		}
	}
	return blocks
}

func claudeToolUseForViewer(use claude.ToolUse) *SessionToolUseWire {
	return &SessionToolUseWire{
		Tool:            use.Tool,
		Input:           use.Input,
		Timestamp:       formatOptionalTime(use.Timestamp),
		CWD:             use.CWD,
		SessionID:       use.SessionID,
		ToolUseID:       use.ToolUseID,
		Source:          use.Source,
		Model:           use.Model,
		ReasoningEffort: use.ReasoningEffort,
		Response:        use.Response,
	}
}

func codexToolUseForViewer(use history.ToolUse) *SessionToolUseWire {
	return &SessionToolUseWire{
		Tool:            use.Tool,
		Input:           use.Input,
		Timestamp:       formatOptionalTime(use.Timestamp),
		CWD:             use.CWD,
		SessionID:       use.SessionID,
		ToolUseID:       use.ToolUseID,
		Source:          use.Source,
		Model:           use.Model,
		ReasoningEffort: use.ReasoningEffort,
		Response:        use.Response,
	}
}

func rawJSONMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func sessionRecordKey(source, path string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + path))
	return source + "-" + hex.EncodeToString(sum[:])[:16]
}

func extendSessionRange(record *SessionRecord, ts time.Time) {
	if ts.IsZero() {
		return
	}
	if record.StartedAt == nil || ts.Before(*record.StartedAt) {
		t := ts
		record.StartedAt = &t
	}
	if record.EndedAt == nil || ts.After(*record.EndedAt) {
		t := ts
		record.EndedAt = &t
	}
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

func formatOptionalTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func fallbackUUID(base string, entryIndex, blockIndex int) string {
	if base != "" {
		return fmt.Sprintf("%s-tool-%d", base, blockIndex)
	}
	return fmt.Sprintf("entry-%d-tool-%d", entryIndex, blockIndex)
}
