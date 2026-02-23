package claude

import (
	"os"
	"path/filepath"
	"strings"
	"time"
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
	MarkerFile string
}

// FindProjectRoot walks up from dir looking for project marker files
func FindProjectRoot(dir string) string {
	info := FindProjectInfo(dir)
	return info.Root
}

// FindProjectInfo walks up from dir looking for project marker files and returns details
func FindProjectInfo(dir string) ProjectInfo {
	if dir == "" {
		return ProjectInfo{}
	}
	current := dir
	for {
		for _, marker := range projectMarkers {
			if _, err := os.Stat(filepath.Join(current, marker)); err == nil {
				return ProjectInfo{Root: current, MarkerFile: marker}
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ProjectInfo{Root: dir}
		}
		current = parent
	}
}

// FindSessionFiles discovers Claude Code session JSONL files in the projects directory.
// If searchAll is false, it only searches for sessions matching the currentDir path.
func FindSessionFiles(projectsDir, currentDir string, searchAll bool) ([]string, error) {
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return nil, nil
	}

	var sessionFiles []string

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, err
	}

	var normalized string
	if !searchAll && currentDir != "" {
		normalized = NormalizePath(currentDir)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		projectPath := filepath.Join(projectsDir, entry.Name())

		if !searchAll && currentDir != "" {
			if !strings.HasSuffix(entry.Name(), normalized) {
				continue
			}
		}

		matches, err := filepath.Glob(filepath.Join(projectPath, "*.jsonl"))
		if err != nil {
			continue
		}

		sessionFiles = append(sessionFiles, matches...)
	}

	return sessionFiles, nil
}

// ParseResult contains the results of parsing Claude Code session history
type ParseResult struct {
	ToolUses        []ToolUse
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

	result := &ParseResult{
		SessionsFound: len(sessionFiles),
	}

	if len(sessionFiles) == 0 {
		return result, nil
	}

	var allToolUses []ToolUse
	for _, sessionFile := range sessionFiles {
		entries, err := ReadHistoryFile(sessionFile)
		if err != nil {
			continue
		}
		if len(entries) > 0 {
			result.SessionsScanned++
			projectPath := ExtractProjectPath(sessionFile)
			projectRoot := FindProjectRoot(projectPath)
			toolUses := ExtractToolUses(entries)
			for i := range toolUses {
				if toolUses[i].CWD == "" {
					toolUses[i].CWD = projectPath
				}
				if toolUses[i].ProjectRoot == "" {
					toolUses[i].ProjectRoot = projectRoot
				}
			}
			allToolUses = append(allToolUses, toolUses...)
		}
	}

	result.ToolUses = FilterToolUses(allToolUses, filter)
	return result, nil
}

type SessionCost struct {
	SessionID string       `json:"sessionId"`
	Project   string       `json:"project"`
	Model     string       `json:"model"`
	Tier      string       `json:"tier"`
	Start     time.Time    `json:"start"`
	End       time.Time    `json:"end"`
	Tokens    TokenSummary `json:"tokens"`
	Messages  int          `json:"messages"`
	Files     []string     `json:"files,omitempty"`
}

func ParseCosts(currentDir string, searchAll bool, since *time.Time) ([]SessionCost, error) {
	sessionFiles, err := FindSessionFiles(GetProjectsDir(), currentDir, searchAll)
	if err != nil {
		return nil, err
	}

	type sessionKey struct {
		sessionID string
		file      string
	}

	costs := make(map[sessionKey]*SessionCost)
	filesets := make(map[sessionKey]map[string]bool)
	var order []sessionKey

	for _, sessionFile := range sessionFiles {
		projectPath := ExtractProjectPath(sessionFile)
		projectRoot := FindProjectRoot(projectPath)
		project := filepath.Base(projectRoot)

		entries, err := ReadHistoryFile(sessionFile)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			ts, _ := entry.ParseTimestamp()
			if since != nil && !ts.IsZero() && ts.Before(*since) {
				continue
			}

			key := sessionKey{sessionID: entry.SessionID, file: sessionFile}

			// Collect file paths from tool uses in all messages
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
		result = append(result, *sc)
	}
	return result, nil
}
