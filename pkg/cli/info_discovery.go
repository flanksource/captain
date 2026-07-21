package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
)

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

func markCurrentSession(sessions []SessionInfo) {
	if len(sessions) > 0 {
		sessions[0].Current = true
	}
}

func sortSessionsRecent(s []SessionInfo) {
	sort.Slice(s, func(i, j int) bool {
		return startedAtSort(s[i]).After(startedAtSort(s[j]))
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

func codexSessionMatchesProject(uses []history.ToolUse, projectRoot string) bool {
	if projectRoot == "" {
		return true
	}
	for _, u := range uses {
		if codexCWDMatchesProject(u.CWD, projectRoot) {
			return true
		}
	}
	return false
}

func codexCWDMatchesProject(cwd, projectRoot string) bool {
	if projectRoot == "" {
		return true
	}
	if cwd == "" {
		return false
	}
	rootAbs := canonicalPath(projectRoot)
	cwdAbs := canonicalPath(cwd)
	if cwdAbs == rootAbs {
		return true
	}
	rel, err := filepath.Rel(rootAbs, cwdAbs)
	return err == nil && rel != ".." && !startsWithParent(rel)
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
