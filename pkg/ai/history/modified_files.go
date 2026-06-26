package history

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/commons/logger"
)

// fileMutatingTools are the Claude Code tools whose tool_use input names a file
// the agent wrote to. Read/Bash/Grep/Glob/etc. are excluded — they observe or
// run, they do not modify a tracked file.
var fileMutatingTools = map[string]string{
	"Edit":         "file_path",
	"Write":        "file_path",
	"MultiEdit":    "file_path", // single-file: many edits, one file_path
	"NotebookEdit": "notebook_path",
}

// ModifiedFiles returns the distinct files an agent wrote to across the given
// tool uses, in first-seen order. Only Edit/Write/MultiEdit/NotebookEdit count;
// the path is read from the tool's input key (file_path / notebook_path). Empty
// or non-string paths are skipped.
func ModifiedFiles(toolUses []ToolUse) []string {
	var files []string
	seen := make(map[string]struct{}, len(toolUses))
	for _, tu := range toolUses {
		key, ok := fileMutatingTools[tu.Tool]
		if !ok {
			continue
		}
		path, _ := tu.Input[key].(string)
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}
	return files
}

// SessionModifiedFiles parses a Claude session log and returns the distinct
// files the agent wrote to during it, in first-seen order.
func SessionModifiedFiles(sessionFile string) ([]string, error) {
	toolUses, err := ExtractToolUses(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("extract tool uses from %s: %w", sessionFile, err)
	}
	return ModifiedFiles(toolUses), nil
}

// FindSessionFile locates the on-disk Claude session log for a session id by
// globbing every project directory (Claude keys session logs by id, so the id
// alone identifies the file regardless of which cwd the agent ran in). When more
// than one project dir holds a log for the id, the most recently modified one is
// returned and the ambiguity is logged.
func FindSessionFile(sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("session id is required")
	}
	projects := GetProjectsDir()
	if projects == "" {
		return "", fmt.Errorf("could not resolve claude projects directory")
	}
	matches, err := filepath.Glob(filepath.Join(projects, "*", sessionID+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("glob session %s under %s: %w", sessionID, projects, err)
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session log found for %s under %s", sessionID, projects)
	case 1:
		return matches[0], nil
	default:
		newest, newestMod := matches[0], int64(-1)
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil {
				continue
			}
			if mod := info.ModTime().UnixNano(); mod > newestMod {
				newest, newestMod = m, mod
			}
		}
		logger.Warnf("session %s matched %d logs; using most recent %s", sessionID, len(matches), newest)
		return newest, nil
	}
}
