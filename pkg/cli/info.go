package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

type InfoOptions struct {
	Path   string `flag:"path" help:"Path to check (defaults to current directory)" short:"p"`
	All    bool   `flag:"all" help:"Include sessions from all projects, not just the current path" short:"a"`
	Claude bool   `flag:"claude" help:"Show only Claude sessions"`
	Codex  bool   `flag:"codex" help:"Show only Codex sessions"`
	Agents bool   `flag:"agents" help:"Count tool calls from nested sub-agents (Task/Agent); --agents=false for the main thread only" default:"true"`
}

// SessionInfo describes a single session's start metadata.
type SessionInfo struct {
	ID              string     `json:"id"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	EndedAt         *time.Time `json:"endedAt,omitempty"`
	Model           string     `json:"model,omitempty"`
	ReasoningEffort string     `json:"reasoningEffort,omitempty"`
	Version         string     `json:"version,omitempty"`
	GitBranch       string     `json:"gitBranch,omitempty"`
	Provider        string     `json:"provider,omitempty"`
	CWD             string     `json:"cwd,omitempty"`
	ToolCalls       int        `json:"toolCalls"`
	Current         bool       `json:"current,omitempty"`
}

// SourceStats summarizes session history for a single agent source (claude or codex).
type SourceStats struct {
	SessionCount   int           `json:"sessionCount"`
	HistoryStart   *time.Time    `json:"historyStart,omitempty"`
	HistoryEnd     *time.Time    `json:"historyEnd,omitempty"`
	TotalToolCalls int           `json:"totalToolCalls"`
	Sessions       []SessionInfo `json:"sessions,omitempty"`
}

func (s SourceStats) IsEmpty() bool {
	return s.SessionCount == 0 && s.TotalToolCalls == 0 && s.HistoryStart == nil
}

// Pretty renders a single line summary for a source's stats.
func (s SourceStats) Pretty() api.Text {
	if s.IsEmpty() {
		return clicky.Text("(no sessions found)", "text-gray-500 italic")
	}
	t := api.Text{}.
		Append(fmt.Sprintf("%d", s.SessionCount), "font-bold").
		Append(" sessions", "text-gray-600").
		Append(" · ", "text-gray-400").
		Append(fmt.Sprintf("%d", s.TotalToolCalls), "font-bold").
		Append(" tool calls", "text-gray-600")
	if s.HistoryStart != nil && s.HistoryEnd != nil {
		t = t.Append(" · ", "text-gray-400").
			Append(api.Human(*s.HistoryStart, "text-gray-500")).
			Append(" → ", "text-gray-400").
			Append(api.Human(*s.HistoryEnd, "text-gray-500"))
	}
	return t
}

// Pretty renders a session start indicator: timestamp, model, version, branch.
// The detected current session shows its full (copyable) ID and a "current" badge;
// others show a short ID.
func (s SessionInfo) Pretty() api.Text {
	t := api.Text{}.Add(icons.Play).Space()
	if s.StartedAt != nil {
		t = t.Append(s.StartedAt.Format("2006-01-02 15:04"), "text-gray-700 font-medium")
	} else {
		t = t.Append("(no timestamp)", "text-gray-500 italic")
	}
	if s.Model != "" {
		t = t.Append("  ", "").
			Add(icons.AI).Space().
			Append(s.Model, "text-purple-600 font-medium")
	} else if s.Provider != "" {
		t = t.Append("  ", "").
			Append(s.Provider, "text-purple-600 font-medium")
	}
	if s.ReasoningEffort != "" {
		t = t.Append(" / ", "text-gray-400").
			Append(s.ReasoningEffort, "text-amber-600")
	}
	if s.Version != "" {
		t = t.Append("  v", "text-gray-500").
			Append(s.Version, "text-gray-600")
	}
	if s.GitBranch != "" {
		t = t.Append("  ", "").
			Add(icons.Git).Space().
			Append(s.GitBranch, "text-green-600")
	}
	if s.ToolCalls > 0 {
		t = t.Append("  ", "").
			Append(fmt.Sprintf("%d calls", s.ToolCalls), "text-gray-500")
	}
	if s.ID != "" {
		id := shortID(s.ID)
		if s.Current {
			id = s.ID
		}
		t = t.Append("  ", "").
			Append(id, "text-gray-400 italic")
	}
	if s.Current {
		t = t.Append("  ", "").
			Append("current", "text-green-600 font-bold")
	}
	return t
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

type InfoResult struct {
	CWD            string      `json:"cwd"`
	ProjectRoot    string      `json:"projectRoot"`
	ProjectName    string      `json:"projectName"`
	MarkerFile     string      `json:"markerFile"`
	ClaudeDir      string      `json:"claudeDir,omitempty"`
	CodexDir       string      `json:"codexDir,omitempty"`
	Claude         SourceStats `json:"claude,omitempty"`
	Codex          SourceStats `json:"codex,omitempty"`
	TotalSessions  int         `json:"totalSessions"`
	TotalToolCalls int         `json:"totalToolCalls"`

	showClaude bool
	showCodex  bool
}

func (r InfoResult) Pretty() api.Text {
	t := api.Text{}.
		Add(icons.Folder).Space().
		Append(displayName(r), "font-bold text-blue-600")
	if r.MarkerFile != "" {
		t = t.Append("  ", "").
			Append(fmt.Sprintf("(%s)", r.MarkerFile), "text-gray-500 italic")
	}

	t = t.NewLine().Add(infoRow("CWD", r.CWD))
	if r.ProjectRoot != "" && r.ProjectRoot != r.CWD {
		t = t.NewLine().Add(infoRow("Root", r.ProjectRoot))
	}

	if r.showClaude {
		t = t.NewLine().NewLine().Add(renderSource(icons.AI, "Claude", r.ClaudeDir, r.Claude))
	}
	if r.showCodex {
		t = t.NewLine().NewLine().
			Add(renderSource(icons.Icon{Unicode: "🤖", Iconify: "mdi:robot", Style: "muted"}, "Codex", r.CodexDir, r.Codex))
	}

	if r.showClaude && r.showCodex {
		t = t.NewLine().NewLine().
			Add(icons.Info).Space().
			Append("Total: ", "text-gray-600").
			Append(fmt.Sprintf("%d sessions", r.TotalSessions), "font-bold").
			Append(" · ", "text-gray-400").
			Append(fmt.Sprintf("%d tool calls", r.TotalToolCalls), "font-bold")
	}

	return t
}

func renderSource(icon api.Textable, name, dir string, stats SourceStats) api.Text {
	t := api.Text{}.
		Add(icon).Space().
		Append(name, "font-bold text-blue-600").
		Append("  ", "").
		Add(stats.Pretty())
	if dir != "" {
		t = t.NewLine().
			Append("    ", "").
			Append(dir, "text-gray-500 italic")
	}
	for _, s := range stats.Sessions {
		t = t.NewLine().Append("    ", "").Add(s.Pretty())
	}
	return t
}

func displayName(r InfoResult) string {
	if r.ProjectName != "" {
		return r.ProjectName
	}
	return filepath.Base(r.CWD)
}

func infoRow(label, value string) api.Text {
	return api.Text{}.
		Append("  "+label+": ", "text-gray-500").
		Append(value, "text-gray-700")
}

func RunInfo(opts InfoOptions) (any, error) {
	path := opts.Path
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}

	// If neither flag is set, show both. If only one is set, hide the other.
	showClaude := opts.Claude || (!opts.Claude && !opts.Codex)
	showCodex := opts.Codex || (!opts.Claude && !opts.Codex)

	projectInfo := claude.FindProjectInfo(path)
	matchRoot := projectInfo.Root
	if matchRoot == "" {
		matchRoot = path
	}

	result := InfoResult{
		CWD:         path,
		ProjectRoot: projectInfo.Root,
		MarkerFile:  projectInfo.MarkerFile,
		showClaude:  showClaude,
		showCodex:   showCodex,
	}

	if projectInfo.Root != "" {
		result.ProjectName = filepath.Base(projectInfo.Root)
	}

	if showClaude {
		result.Claude = collectClaudeStats(path, opts.All, opts.Agents, &result)
	}
	if showCodex {
		result.Codex = collectCodexStats(matchRoot, opts.All, &result)
	}

	// The most-recent session for the current directory is the detected
	// "current" session. Marking it surfaces its full ID for use with
	// `captain history --session` and `captain changes --session-id`.
	if !opts.All {
		markCurrentSession(result.Claude.Sessions)
		markCurrentSession(result.Codex.Sessions)
	}

	result.TotalSessions = result.Claude.SessionCount + result.Codex.SessionCount
	result.TotalToolCalls = result.Claude.TotalToolCalls + result.Codex.TotalToolCalls

	return result, nil
}

func collectClaudeStats(path string, searchAll, includeAgents bool, result *InfoResult) SourceStats {
	stats := SourceStats{}

	projectsDir := claude.GetProjectsDir()
	normalized := claude.NormalizePath(path)
	claudeProjectDir := filepath.Join(projectsDir, normalized)
	if _, err := os.Stat(claudeProjectDir); err == nil {
		result.ClaudeDir = claudeProjectDir
	}

	sessionFiles, err := claude.FindSessionFiles(projectsDir, path, searchAll)
	if err != nil || len(sessionFiles) == 0 {
		return stats
	}
	if !searchAll {
		result.ClaudeDir = filepath.Dir(sessionFiles[0])
	}
	stats.SessionCount = len(sessionFiles)

	for _, sessionFile := range sessionFiles {
		entries, err := claude.ReadHistoryFile(sessionFile)
		if err != nil {
			continue
		}

		session := SessionInfo{ID: sessionIDFromFile(sessionFile)}
		for _, entry := range entries {
			ts, err := entry.ParseTimestamp()
			if err == nil {
				updateRange(&stats, ts)
				if session.StartedAt == nil || ts.Before(*session.StartedAt) {
					t := ts
					session.StartedAt = &t
				}
				if session.EndedAt == nil || ts.After(*session.EndedAt) {
					t := ts
					session.EndedAt = &t
				}
			}
			if session.Model == "" && entry.Message.Model != "" {
				session.Model = entry.Message.Model
			}
			if session.Version == "" && entry.Version != "" {
				session.Version = entry.Version
			}
			if session.GitBranch == "" && entry.GitBranch != "" {
				session.GitBranch = entry.GitBranch
			}
			if session.CWD == "" && entry.CWD != "" {
				session.CWD = entry.CWD
			}
			if session.ID == "" && entry.SessionID != "" {
				session.ID = entry.SessionID
			}
			toolCount := len(entry.Message.GetToolUses())
			session.ToolCalls += toolCount
			stats.TotalToolCalls += toolCount
		}
		stats.Sessions = append(stats.Sessions, session)
	}

	if includeAgents {
		addAgentToolCalls(&stats, projectsDir, path, searchAll)
	}

	sortSessionsRecent(stats.Sessions)
	return stats
}

// addAgentToolCalls folds nested sub-agent (Task/Agent) tool calls into the
// total and into their parent session's count, without inflating SessionCount —
// sub-agents belong to the session that spawned them, not to new sessions.
func addAgentToolCalls(stats *SourceStats, projectsDir, path string, searchAll bool) {
	agentFiles, err := claude.FindAgentTranscripts(projectsDir, path, searchAll)
	if err != nil {
		return
	}
	byID := make(map[string]*SessionInfo, len(stats.Sessions))
	for i := range stats.Sessions {
		byID[stats.Sessions[i].ID] = &stats.Sessions[i]
	}
	for _, af := range agentFiles {
		entries, err := claude.ReadHistoryFile(af)
		if err != nil {
			continue
		}
		count, parentID := 0, ""
		for _, entry := range entries {
			count += len(entry.Message.GetToolUses())
			if parentID == "" && entry.SessionID != "" {
				parentID = entry.SessionID
			}
			if ts, err := entry.ParseTimestamp(); err == nil {
				updateRange(stats, ts)
			}
		}
		stats.TotalToolCalls += count
		if s := byID[parentID]; s != nil {
			s.ToolCalls += count
		}
	}
}

func collectCodexStats(projectRoot string, searchAll bool, result *InfoResult) SourceStats {
	stats := SourceStats{}

	home, err := os.UserHomeDir()
	if err == nil {
		codexSessionsDir := filepath.Join(home, ".codex", "sessions")
		if _, err := os.Stat(codexSessionsDir); err == nil {
			result.CodexDir = codexSessionsDir
		}
	}

	codexFiles, err := history.FindCodexSessionFiles()
	if err != nil || len(codexFiles) == 0 {
		return stats
	}

	for _, file := range codexFiles {
		uses, err := history.ExtractCodexToolUses(file)
		if err != nil || len(uses) == 0 {
			continue
		}
		if !searchAll && !codexSessionMatchesProject(uses, projectRoot) {
			continue
		}
		stats.SessionCount++
		stats.TotalToolCalls += len(uses)

		session := SessionInfo{ToolCalls: len(uses)}
		for _, u := range uses {
			if u.Timestamp != nil {
				updateRange(&stats, *u.Timestamp)
				if session.StartedAt == nil || u.Timestamp.Before(*session.StartedAt) {
					t := *u.Timestamp
					session.StartedAt = &t
				}
				if session.EndedAt == nil || u.Timestamp.After(*session.EndedAt) {
					t := *u.Timestamp
					session.EndedAt = &t
				}
			}
			if session.ID == "" && u.SessionID != "" {
				session.ID = u.SessionID
			}
			if session.CWD == "" && u.CWD != "" {
				session.CWD = u.CWD
			}
		}

		if meta, err := history.ReadCodexSessionInfo(file); err == nil && meta != nil {
			if session.ID == "" {
				session.ID = meta.ID
			}
			session.Provider = meta.ModelProvider
			session.Version = meta.CLIVersion
			session.GitBranch = meta.GitBranch
			session.Model = meta.Model
			session.ReasoningEffort = meta.ReasoningEffort
			if session.StartedAt == nil && meta.StartedAt != nil {
				session.StartedAt = meta.StartedAt
			}
		}

		stats.Sessions = append(stats.Sessions, session)
	}
	sortSessionsRecent(stats.Sessions)
	return stats
}

// markCurrentSession flags the most-recent session (sessions are sorted
// recent-first) as the detected current session.
func markCurrentSession(sessions []SessionInfo) {
	if len(sessions) > 0 {
		sessions[0].Current = true
	}
}

func sortSessionsRecent(s []SessionInfo) {
	sort.Slice(s, func(i, j int) bool {
		ti := startedAtSort(s[i])
		tj := startedAtSort(s[j])
		return ti.After(tj)
	})
}

func startedAtSort(s SessionInfo) time.Time {
	if s.StartedAt != nil {
		return *s.StartedAt
	}
	return time.Time{}
}

func sessionIDFromFile(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// codexSessionMatchesProject returns true when at least one tool use in the
// session reports a CWD that lives within the given project root. Codex stores
// the cwd in the session_meta event, which is propagated onto every ToolUse.
func codexSessionMatchesProject(uses []history.ToolUse, projectRoot string) bool {
	if projectRoot == "" {
		return true
	}
	rootAbs := canonicalPath(projectRoot)
	for _, u := range uses {
		if u.CWD == "" {
			continue
		}
		cwdAbs := canonicalPath(u.CWD)
		if cwdAbs == rootAbs {
			return true
		}
		rel, err := filepath.Rel(rootAbs, cwdAbs)
		if err == nil && rel != ".." && !startsWithParent(rel) {
			return true
		}
	}
	return false
}

func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func startsWithParent(rel string) bool {
	return len(rel) >= 2 && rel[:2] == ".."
}

func updateRange(stats *SourceStats, ts time.Time) {
	if stats.HistoryStart == nil || ts.Before(*stats.HistoryStart) {
		t := ts
		stats.HistoryStart = &t
	}
	if stats.HistoryEnd == nil || ts.After(*stats.HistoryEnd) {
		t := ts
		stats.HistoryEnd = &t
	}
}
