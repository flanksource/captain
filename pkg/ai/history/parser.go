package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons/collections"
	"github.com/flanksource/commons/logger"
)

func NormalizePath(path string) string {
	normalized := strings.ReplaceAll(path, "/", "-")
	return strings.ReplaceAll(normalized, ".", "-")
}

func FindSessionFiles(projectsDir, currentDir string, searchAll bool) ([]string, error) {
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		logger.Debugf("Projects directory does not exist: %s", projectsDir)
		return nil, nil
	}

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, err
	}

	logger.Debugf("Found %d project directories in %s", len(entries), projectsDir)

	var normalized string
	if !searchAll && currentDir != "" {
		normalized = NormalizePath(currentDir)
		logger.Debugf("Looking for directories matching: %s", normalized)
	}

	var sessionFiles []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		projectPath := filepath.Join(projectsDir, entry.Name())

		if !searchAll && currentDir != "" {
			if !strings.HasSuffix(entry.Name(), normalized) {
				continue
			}
			logger.Debugf("Matched directory: %s", entry.Name())
		}

		matches, err := filepath.Glob(filepath.Join(projectPath, "*.jsonl"))
		if err != nil {
			logger.Warnf("Error globbing session files in %s: %v", projectPath, err)
			continue
		}

		logger.Debugf("Found %d session files in %s", len(matches), projectPath)
		sessionFiles = append(sessionFiles, matches...)
	}

	logger.Debugf("Total session files found: %d", len(sessionFiles))
	return sessionFiles, nil
}

func ExtractToolUses(sessionFile string) ([]ToolUse, error) {
	file, err := os.Open(sessionFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var toolUses []ToolUse
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry SessionEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			logger.Debugf("Error parsing line in %s: %v", sessionFile, err)
			continue
		}

		for _, content := range entry.Message.Content {
			if content.Type != "tool_use" {
				continue
			}

			var timestamp *time.Time
			if entry.Timestamp != "" {
				if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
					timestamp = &t
				}
			}

			toolUses = append(toolUses, ToolUse{
				Tool:      content.Name,
				Input:     content.Input,
				Timestamp: timestamp,
				CWD:       entry.CWD,
				SessionID: entry.SessionID,
				ToolUseID: content.ID,
				Source:    "claude",
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return toolUses, nil
}

func FormatCommand(toolUse ToolUse) string {
	switch toolUse.Tool {
	case "Bash", "CodexCommand":
		if cmd, ok := toolUse.Input["command"].(string); ok {
			return cmd
		}
	case "Read", "Write", "Edit":
		if path, ok := toolUse.Input["file_path"].(string); ok {
			return path
		}
	case "Grep":
		pattern, _ := toolUse.Input["pattern"].(string)
		path, _ := toolUse.Input["path"].(string)
		return strings.TrimSpace(fmt.Sprintf("%s %s", pattern, path))
	case "Glob":
		if pattern, ok := toolUse.Input["pattern"].(string); ok {
			return pattern
		}
	case "WebFetch":
		if url, ok := toolUse.Input["url"].(string); ok {
			return url
		}
	}
	b, _ := json.Marshal(toolUse.Input)
	return string(b)
}

func FilterToolUses(toolUses []ToolUse, filter Filter) []ToolUse {
	var filtered []ToolUse

	for _, tu := range toolUses {
		if filter.Source != "" && tu.Source != filter.Source {
			continue
		}

		if filter.Tool != "" {
			patterns := strings.Split(filter.Tool, ",")
			for i, p := range patterns {
				patterns[i] = strings.TrimSpace(p)
			}
			matched, negated := collections.MatchAny([]string{tu.Tool}, patterns...)
			if negated || !matched {
				continue
			}
		}

		if filter.Since != nil && tu.Timestamp != nil && tu.Timestamp.Before(*filter.Since) {
			continue
		}
		if filter.Before != nil && tu.Timestamp != nil && tu.Timestamp.After(*filter.Before) {
			continue
		}

		filtered = append(filtered, tu)
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Timestamp == nil {
			return false
		}
		if filtered[j].Timestamp == nil {
			return true
		}
		return filtered[i].Timestamp.After(*filtered[j].Timestamp)
	})

	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}

	return filtered
}

func GetClaudeHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

func GetProjectsDir() string {
	return filepath.Join(GetClaudeHome(), "projects")
}

type ParseResult struct {
	ToolUses        []ToolUse
	SessionsFound   int
	SessionsScanned int
}

func ParseHistory(currentDir string, searchAll bool, filter Filter) (*ParseResult, error) {
	result := &ParseResult{}
	var allToolUses []ToolUse

	if filter.Source == "" || filter.Source == "claude" {
		claudeFiles, err := FindSessionFiles(GetProjectsDir(), currentDir, searchAll)
		if err != nil {
			return nil, err
		}
		result.SessionsFound += len(claudeFiles)

		for _, f := range claudeFiles {
			toolUses, err := ExtractToolUses(f)
			if err != nil {
				logger.Warnf("Error extracting tool uses from %s: %v", f, err)
				continue
			}
			if len(toolUses) > 0 {
				result.SessionsScanned++
				allToolUses = append(allToolUses, toolUses...)
			}
		}
	}

	if filter.Source == "" || filter.Source == "codex" {
		codexFiles, err := FindCodexSessionFiles()
		if err != nil {
			logger.Warnf("Error finding codex sessions: %v", err)
		} else {
			result.SessionsFound += len(codexFiles)
			for _, f := range codexFiles {
				toolUses, err := ExtractCodexToolUses(f)
				if err != nil {
					logger.Warnf("Error extracting codex tool uses from %s: %v", f, err)
					continue
				}
				if len(toolUses) > 0 {
					result.SessionsScanned++
					allToolUses = append(allToolUses, toolUses...)
				}
			}
		}
	}

	result.ToolUses = FilterToolUses(allToolUses, filter)
	return result, nil
}
