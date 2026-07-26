package claude

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/segmentio/encoding/json"
)

// GetClaudeHome returns the path to the Claude Code home directory (~/.claude)
func GetClaudeHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// GetProjectsDir returns the path to the Claude Code projects directory (~/.claude/projects)
func GetProjectsDir() string {
	return filepath.Join(GetClaudeHome(), "projects")
}

// NormalizePath converts a filesystem path into a normalized format
// by replacing "/", ".", and "_" with "-" for use as a directory name
// (matching Claude Code's normalization behavior)
func NormalizePath(path string) string {
	normalized := strings.ReplaceAll(path, "/", "-")
	normalized = strings.ReplaceAll(normalized, ".", "-")
	normalized = strings.ReplaceAll(normalized, "_", "-")
	return normalized
}

var projectMarkers = []string{
	"go.mod", "go.sum",
	"package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
	"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle",
	"Cargo.toml", "Cargo.lock",
	"pyproject.toml", "setup.py", "requirements.txt",
	"Gemfile", "Gemfile.lock",
	"composer.json",
	"Makefile", "CMakeLists.txt",
	".git",
}

// ProjectInfo contains information about a detected project
type ProjectInfo struct {
	Root       string
	MainRoot   string // For worktrees, the main repo root; empty otherwise
	MarkerFile string
}

// FindProjectRoot walks up from dir looking for project marker files
func FindProjectRoot(dir string) string {
	info := FindProjectInfo(dir)
	return info.Root
}

// FindProjectInfo walks up from dir looking for project marker files and returns details.
// For git worktrees (where .git is a file), Root is the worktree directory (for correct
// relative paths) and MainRoot is the main repository root (for project naming).
func FindProjectInfo(dir string) ProjectInfo {
	if dir == "" {
		return ProjectInfo{}
	}
	current := dir
	for {
		entries, err := os.ReadDir(current)
		if err == nil {
			entriesByName := make(map[string]os.DirEntry)
			for _, entry := range entries {
				entriesByName[entry.Name()] = entry
			}
			for _, marker := range projectMarkers {
				entry, ok := entriesByName[marker]
				if !ok || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				pi := ProjectInfo{Root: current, MarkerFile: marker}
				if marker == ".git" && !entry.IsDir() {
					pi.MainRoot = resolveWorktreeRoot(filepath.Join(current, marker))
				}
				return pi
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ProjectInfo{Root: dir}
		}
		current = parent
	}
}

// resolveWorktreeRoot reads a .git file (used by worktrees) and resolves
// the main repository root. A worktree .git file contains:
//
//	gitdir: /path/to/main-repo/.git/worktrees/<name>
//
// This function extracts the main repo root from that path.
func resolveWorktreeRoot(gitFilePath string) string {
	if filepath.Base(gitFilePath) != ".git" {
		return ""
	}
	root, err := os.OpenRoot(filepath.Dir(gitFilePath))
	if err != nil {
		return ""
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(".git")
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return ""
	}
	line := strings.TrimSpace(scanner.Text())
	if scanner.Scan() || scanner.Err() != nil {
		return ""
	}
	if !strings.HasPrefix(line, "gitdir: ") {
		return ""
	}
	gitdir := strings.TrimPrefix(line, "gitdir: ")
	// gitdir looks like: /path/to/repo/.git/worktrees/<name>
	// Walk up to find the .git directory, then its parent is the repo root
	parts := strings.Split(filepath.ToSlash(gitdir), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == ".git" {
			root := filepath.FromSlash(strings.Join(parts[:i], "/"))
			if _, err := os.Stat(root); err == nil {
				return root
			}
			break
		}
	}
	return ""
}

// FindSessionFiles discovers Claude Code session JSONL files in the projects directory.
// If searchAll is false, it only searches for sessions matching the currentDir path.
func FindSessionFiles(projectsDir, currentDir string, searchAll bool) ([]string, error) {
	return findProjectFiles(projectsDir, currentDir, searchAll, "*.jsonl")
}

// FindAgentTranscripts returns the nested sub-agent JSONL files for the in-scope
// sessions: <projectPath>/<session-uuid>/subagents/agent-*.jsonl. Agents at any
// spawn depth land flat in that one directory, so a single glob covers nesting.
func FindAgentTranscripts(projectsDir, currentDir string, searchAll bool) ([]string, error) {
	return findProjectFiles(projectsDir, currentDir, searchAll, "*", "subagents", "agent-*.jsonl")
}

func filterSessionFilesBySessionID(files []string, filter Filter) []string {
	if !filter.HasSessionIDFilter() {
		return files
	}
	out := make([]string, 0, len(files))
	for _, file := range files {
		if filter.MatchesSessionID(sessionIDFromTranscriptPath(file)) {
			out = append(out, file)
		}
	}
	return out
}

func sessionIDFromTranscriptPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		if part == "subagents" && i > 0 {
			return parts[i-1]
		}
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// findProjectFiles globs each in-scope project directory under projectsDir for
// files matching the given path segments (joined onto the project dir).
func findProjectFiles(projectsDir, currentDir string, searchAll bool, globParts ...string) ([]string, error) {
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, err
	}

	var normalized string
	if !searchAll && currentDir != "" {
		normalized = NormalizePath(currentDir)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if !searchAll && currentDir != "" {
			if !hasSuffixFold(entry.Name(), normalized) {
				continue
			}
		}

		projectPath := filepath.Join(projectsDir, entry.Name())
		pattern := filepath.Join(append([]string{projectPath}, globParts...)...)
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		files = append(files, matches...)
	}

	return files, nil
}

// isAgentTranscript reports whether a session file is a nested sub-agent transcript
// (lives under a session's subagents/ directory).
func isAgentTranscript(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/subagents/")
}

// agentMeta is the sidecar JSON Claude Code writes next to each sub-agent
// transcript (agent-<id>.meta.json).
type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
}

// readAgentMeta loads the agentType/description from the agent-<id>.meta.json
// sibling of a sub-agent transcript. Missing/unreadable meta yields empty strings.
func readAgentMeta(transcriptPath string) (agentType, desc string) {
	metaPath := strings.TrimSuffix(transcriptPath, ".jsonl") + ".meta.json"
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return "", ""
	}
	var m agentMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return "", ""
	}
	return m.AgentType, m.Description
}

// projectPathForFile resolves the original project directory for a session file,
// working for both top-level (<slug>/<uuid>.jsonl) and nested sub-agent
// (<slug>/<uuid>/subagents/agent-*.jsonl) transcripts by denormalizing the slug
// segment directly under projectsDir.
func projectPathForFile(projectsDir, sessionFile string) string {
	rel, err := filepath.Rel(projectsDir, sessionFile)
	if err != nil {
		return ExtractProjectPath(sessionFile)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" || parts[0] == ".." {
		return ExtractProjectPath(sessionFile)
	}
	return DenormalizePath(parts[0])
}

func hasSuffixFold(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return strings.EqualFold(s[len(s)-len(suffix):], suffix)
}

// ParseResult contains the results of parsing Claude Code session history
type ParseResult struct {
	ToolUses        []ToolUse
	Costs           []SessionCost
	SessionsFound   int
	SessionsScanned int
}

// DenormalizePath converts a normalized directory name back to a filesystem path.
// The normalization replaces "/", ".", and "_" with "-", so this function
// tries to reconstruct the original path by checking if paths exist.
func DenormalizePath(normalized string) string {
	if normalized == "" {
		return ""
	}

	// First try simple replacement (all dashes to slashes)
	simple := strings.ReplaceAll(normalized, "-", "/")
	if _, err := os.Stat(simple); err == nil {
		return simple
	}

	// Build path incrementally, checking each segment
	parts := strings.Split(normalized, "-")
	var currentPath string

	for i := 0; i < len(parts); i++ {
		segment := parts[i]

		// Handle common domain patterns
		if i+1 < len(parts) {
			combined := segment + "." + parts[i+1]
			testPath := currentPath + "/" + combined
			if _, err := os.Stat(testPath); err == nil {
				currentPath = testPath
				i++
				continue
			}
		}

		// Try as regular path segment
		testPath := currentPath + "/" + segment
		if _, err := os.Stat(testPath); err == nil {
			currentPath = testPath
			continue
		}

		// Try joining with underscore to previous or next segment
		if i+1 < len(parts) {
			// Try combining current and next with underscore
			combined := segment + "_" + parts[i+1]
			testPath := currentPath + "/" + combined
			if _, err := os.Stat(testPath); err == nil {
				currentPath = testPath
				i++
				continue
			}
		}

		// Default: just add as path segment
		currentPath = currentPath + "/" + segment
	}

	return filepath.Clean(currentPath)
}

// ExtractProjectPath extracts the original project path from a session file path
func ExtractProjectPath(sessionFile string) string {
	dir := filepath.Dir(sessionFile)
	projectDirName := filepath.Base(dir)
	return DenormalizePath(projectDirName)
}

// ParseHistory is the main entry point for parsing Claude Code session history.
// It discovers session files, extracts tool uses, applies filters, and returns aggregated results.
func ParseHistory(currentDir string, searchAll bool, filter Filter) (*ParseResult, error) {
	projectsDir := GetProjectsDir()

	sessionFiles, err := FindSessionFiles(projectsDir, currentDir, searchAll)
	if err != nil {
		return nil, err
	}
	if filter.IncludeAgents {
		if agentFiles, err := FindAgentTranscripts(projectsDir, currentDir, searchAll); err == nil {
			sessionFiles = append(sessionFiles, agentFiles...)
		}
	}
	sessionFiles = filterSessionFilesBySessionID(sessionFiles, filter)

	result := &ParseResult{
		SessionsFound: len(sessionFiles),
	}

	if len(sessionFiles) == 0 {
		return result, nil
	}

	var allToolUses []ToolUse
	for _, sessionFile := range sessionFiles {
		entries, err := ReadHistoryFileWithOptions(sessionFile, ReadOptions{KeepRaw: filter.KeepRaw})
		if err != nil {
			parseLog.Warnf("skipping unreadable transcript %s: %v", sessionFile, err)
			continue
		}
		if len(entries) > 0 {
			result.SessionsScanned++
			projectPath := ExtractProjectPath(sessionFile)
			projectRoot := FindProjectRoot(projectPath)
			if filter.IncludeCosts {
				result.Costs = append(result.Costs, costsFromEntries(sessionFile, entries, projectRoot, filter.Since)...)
			}
			toolUses := stampToolUses(ExtractToolUses(entries), projectsDir, sessionFile)
			allToolUses = append(allToolUses, toolUses...)
		}
	}

	result.ToolUses = FilterToolUses(allToolUses, filter)
	return result, nil
}

// stampToolUses backfills CWD/ProjectRoot and, for sub-agent transcripts, the
// owning agent's type/description (from the agent-<id>.meta.json sidecar).
func stampToolUses(toolUses []ToolUse, projectsDir, sessionFile string) []ToolUse {
	projectPath := ExtractProjectPath(sessionFile)
	var agentType, agentDesc, agentID string
	sidechain := isAgentTranscript(sessionFile)
	if sidechain {
		projectPath = projectPathForFile(projectsDir, sessionFile)
		agentType, agentDesc = readAgentMeta(sessionFile)
		agentID = agentIDFromPath(sessionFile)
	}
	projectRoot := FindProjectRoot(projectPath)
	for i := range toolUses {
		if toolUses[i].CWD == "" {
			toolUses[i].CWD = projectPath
		}
		if toolUses[i].ProjectRoot == "" {
			toolUses[i].ProjectRoot = projectRoot
		}
		if sidechain {
			toolUses[i].IsSidechain = true
		}
		if agentID != "" && toolUses[i].AgentID == "" {
			toolUses[i].AgentID = agentID
		}
		if agentType != "" && toolUses[i].AgentType == "" {
			toolUses[i].AgentType = agentType
		}
		if agentDesc != "" && toolUses[i].AgentDesc == "" {
			toolUses[i].AgentDesc = agentDesc
		}
	}
	return toolUses
}

// agentIDFromPath extracts the agent id from a sub-agent transcript filename
// (".../agent-<id>.jsonl" → "<id>").
func agentIDFromPath(transcriptPath string) string {
	base := strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
	return strings.TrimPrefix(base, "agent-")
}

// ParseHistoryTools is like ParseHistory but returns Tool implementations
// with per-tool model usage and cost data populated from assistant message Usage.
// Use this when callers need cost/token breakdown per tool call.
func ParseHistoryTools(currentDir string, searchAll bool, filter Filter) ([]tools.Tool, error) {
	projectsDir := GetProjectsDir()

	sessionFiles, err := FindSessionFiles(projectsDir, currentDir, searchAll)
	if err != nil {
		return nil, err
	}
	if filter.IncludeAgents {
		if agentFiles, err := FindAgentTranscripts(projectsDir, currentDir, searchAll); err == nil {
			sessionFiles = append(sessionFiles, agentFiles...)
		}
	}
	sessionFiles = filterSessionFilesBySessionID(sessionFiles, filter)

	var allToolUses []ToolUse
	uses := make(map[string][]HistoryEntry)
	for _, sessionFile := range sessionFiles {
		entries, err := ReadHistoryFileWithOptions(sessionFile, ReadOptions{KeepRaw: filter.KeepRaw})
		if err != nil {
			parseLog.Warnf("skipping unreadable transcript %s: %v", sessionFile, err)
			continue
		}
		if len(entries) == 0 {
			continue
		}
		toolUses := stampToolUses(ExtractToolUsesWithTokens(entries), projectsDir, sessionFile)
		allToolUses = append(allToolUses, toolUses...)
		uses[sessionFile] = entries
	}

	filtered := FilterToolUses(allToolUses, filter)

	// Re-link filtered tool uses to entries to populate Models with model and cost.
	// Build a single combined entry list once; toTools indexes by ToolUseID.
	var allEntries []HistoryEntry
	for _, entries := range uses {
		allEntries = append(allEntries, entries...)
	}
	return toTools(filtered, allEntries), nil
}

type SessionCost struct {
	SessionID string             `json:"sessionId"`
	Project   string             `json:"project"`
	Model     string             `json:"model"`
	Tier      string             `json:"tier"`
	Start     time.Time          `json:"start"`
	End       time.Time          `json:"end"`
	Tokens    TokenSummary       `json:"tokens"`
	Messages  int                `json:"messages"`
	Files     []string           `json:"files,omitempty"`
	Context   *ContextBreakdown  `json:"context,omitempty"`
	ToolCosts []ToolTokenSummary `json:"toolCosts,omitempty"`
}

func ParseCosts(currentDir string, searchAll bool, since *time.Time) ([]SessionCost, error) {
	return ParseCostsWithFilter(currentDir, searchAll, since, Filter{})
}

func ParseCostsWithFilter(currentDir string, searchAll bool, since *time.Time, filter Filter) ([]SessionCost, error) {
	sessionFiles, err := FindSessionFiles(GetProjectsDir(), currentDir, searchAll)
	if err != nil {
		return nil, err
	}
	sessionFiles = filterSessionFilesBySessionID(sessionFiles, filter)

	var result []SessionCost
	for _, sessionFile := range sessionFiles {
		projectPath := ExtractProjectPath(sessionFile)
		projectRoot := FindProjectRoot(projectPath)

		entries, err := ReadHistoryFileWithOptions(sessionFile, ReadOptions{})
		if err != nil {
			continue
		}
		result = append(result, costsFromEntries(sessionFile, entries, projectRoot, since)...)
	}
	return result, nil
}

func costsFromEntries(sessionFile string, entries []HistoryEntry, projectRoot string, since *time.Time) []SessionCost {
	type sessionKey struct {
		sessionID string
		file      string
	}

	project := filepath.Base(projectRoot)
	costs := make(map[sessionKey]*SessionCost)
	filesets := make(map[sessionKey]map[string]bool)
	var order []sessionKey

	for _, entry := range entries {
		ts, _ := entry.ParseTimestamp()
		if since != nil && !ts.IsZero() && ts.Before(*since) {
			continue
		}

		key := sessionKey{sessionID: entry.SessionID, file: sessionFile}

		// Collect file paths from tool uses in all messages.
		for _, tu := range ExtractToolUses([]HistoryEntry{entry}) {
			tu.ProjectRoot = projectRoot
			if p := tu.ExtractPath(); p != "" {
				if filesets[key] == nil {
					filesets[key] = make(map[string]bool)
				}
				filesets[key][p] = true
			}
		}

		if !entry.IsAssistantMessage() || entry.Message.Usage == nil {
			continue
		}
		if ts.IsZero() {
			continue
		}

		sc, ok := costs[key]
		if !ok {
			sc = &SessionCost{
				SessionID: entry.SessionID,
				Project:   project,
				Start:     ts,
				End:       ts,
			}
			costs[key] = sc
			order = append(order, key)
		}

		if ts.Before(sc.Start) {
			sc.Start = ts
		}
		if ts.After(sc.End) {
			sc.End = ts
		}

		model := entry.Message.Model
		if model != "" {
			sc.Model = model
		}
		if tier := entry.Message.Usage.ServiceTier; tier != "" {
			sc.Tier = tier
		}

		sc.Tokens.Add(entry.Message.Usage, model)
		sc.Messages++
	}

	result := make([]SessionCost, 0, len(order))
	for _, key := range order {
		sc := costs[key]
		for f := range filesets[key] {
			sc.Files = append(sc.Files, f)
		}
		result = append(result, *sc)
	}
	return result
}

// ParseCostsDetailed extends ParseCosts with context categorization and per-tool token breakdown.
func ParseCostsDetailed(currentDir string, searchAll bool, since *time.Time) ([]SessionCost, error) {
	return ParseCostsDetailedWithFilter(currentDir, searchAll, since, Filter{})
}

// ParseCostsDetailedWithFilter extends ParseCostsWithFilter with context categorization and per-tool token breakdown.
func ParseCostsDetailedWithFilter(currentDir string, searchAll bool, since *time.Time, filter Filter) ([]SessionCost, error) {
	sessionFiles, err := FindSessionFiles(GetProjectsDir(), currentDir, searchAll)
	if err != nil {
		return nil, err
	}
	sessionFiles = filterSessionFilesBySessionID(sessionFiles, filter)

	type sessionKey struct {
		sessionID string
		file      string
	}

	costs := make(map[sessionKey]*SessionCost)
	filesets := make(map[sessionKey]map[string]bool)
	sessionEntries := make(map[sessionKey][]HistoryEntry)
	var order []sessionKey

	for _, sessionFile := range sessionFiles {
		projectPath := ExtractProjectPath(sessionFile)
		projectRoot := FindProjectRoot(projectPath)
		project := filepath.Base(projectRoot)

		entries, err := ReadHistoryFileWithOptions(sessionFile, ReadOptions{})
		if err != nil {
			continue
		}

		for _, entry := range entries {
			ts, _ := entry.ParseTimestamp()
			if since != nil && !ts.IsZero() && ts.Before(*since) {
				continue
			}

			key := sessionKey{sessionID: entry.SessionID, file: sessionFile}
			sessionEntries[key] = append(sessionEntries[key], entry)

			for _, tu := range ExtractToolUses([]HistoryEntry{entry}) {
				tu.ProjectRoot = projectRoot
				if p := tu.ExtractPath(); p != "" {
					if filesets[key] == nil {
						filesets[key] = make(map[string]bool)
					}
					filesets[key][p] = true
				}
			}

			if !entry.IsAssistantMessage() || entry.Message.Usage == nil {
				continue
			}
			if ts.IsZero() {
				continue
			}

			sc, ok := costs[key]
			if !ok {
				sc = &SessionCost{
					SessionID: entry.SessionID,
					Project:   project,
					Start:     ts,
					End:       ts,
				}
				costs[key] = sc
				order = append(order, key)
			}

			if ts.Before(sc.Start) {
				sc.Start = ts
			}
			if ts.After(sc.End) {
				sc.End = ts
			}

			model := entry.Message.Model
			if model != "" {
				sc.Model = model
			}
			if tier := entry.Message.Usage.ServiceTier; tier != "" {
				sc.Tier = tier
			}

			sc.Tokens.Add(entry.Message.Usage, model)
			sc.Messages++
		}
	}

	result := make([]SessionCost, 0, len(order))
	for _, key := range order {
		sc := costs[key]
		for f := range filesets[key] {
			sc.Files = append(sc.Files, f)
		}

		entries := sessionEntries[key]
		cb := CategorizeEntries(entries)
		sc.Context = &cb
		sc.ToolCosts = AggregateByTool(ExtractToolUsesWithTokens(entries))

		result = append(result, *sc)
	}
	return result, nil
}
