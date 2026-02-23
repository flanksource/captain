package cli

import (
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/claude"
)

type InfoOptions struct {
	Path string `flag:"path" help:"Path to check (defaults to current directory)" short:"p"`
}

type InfoResult struct {
	CWD            string     `json:"cwd" pretty:"label=Current Directory"`
	ProjectRoot    string     `json:"projectRoot" pretty:"label=Project Root"`
	ProjectName    string     `json:"projectName" pretty:"label=Project Name"`
	MarkerFile     string     `json:"markerFile" pretty:"label=Detected By"`
	ClaudeDir      string     `json:"claudeDir" pretty:"label=Claude Project Dir"`
	SessionCount   int        `json:"sessionCount" pretty:"label=Sessions"`
	HistoryStart   *time.Time `json:"historyStart,omitempty" pretty:"label=History Start,format=date"`
	HistoryEnd     *time.Time `json:"historyEnd,omitempty" pretty:"label=History End,format=date"`
	TotalToolCalls int        `json:"totalToolCalls" pretty:"label=Total Tool Calls"`
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

	projectInfo := claude.FindProjectInfo(path)

	result := InfoResult{
		CWD:         path,
		ProjectRoot: projectInfo.Root,
		MarkerFile:  projectInfo.MarkerFile,
	}

	if projectInfo.Root != "" {
		result.ProjectName = filepath.Base(projectInfo.Root)
	}

	// Find Claude project directory
	projectsDir := claude.GetProjectsDir()
	normalized := claude.NormalizePath(path)
	claudeProjectDir := filepath.Join(projectsDir, normalized)
	if _, err := os.Stat(claudeProjectDir); err == nil {
		result.ClaudeDir = claudeProjectDir
	}

	// Get session info
	sessionFiles, err := claude.FindSessionFiles(projectsDir, path, false)
	if err == nil && len(sessionFiles) > 0 {
		result.SessionCount = len(sessionFiles)

		// Parse all sessions to get history range
		var earliest, latest *time.Time
		var totalCalls int

		for _, sessionFile := range sessionFiles {
			entries, err := claude.ReadHistoryFile(sessionFile)
			if err != nil {
				continue
			}

			for _, entry := range entries {
				ts, err := entry.ParseTimestamp()
				if err != nil {
					continue
				}
				if earliest == nil || ts.Before(*earliest) {
					earliest = &ts
				}
				if latest == nil || ts.After(*latest) {
					latest = &ts
				}
				totalCalls += len(entry.Message.GetToolUses())
			}
		}

		result.HistoryStart = earliest
		result.HistoryEnd = latest
		result.TotalToolCalls = totalCalls
	}

	return result, nil
}
