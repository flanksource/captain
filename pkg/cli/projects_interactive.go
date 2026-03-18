package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/flanksource/captain/pkg/claude"
)

func runProjectsCleanInteractive(opts ProjectsCleanOptions) (any, error) {
	projects, err := scanProjects()
	if err != nil {
		return nil, err
	}

	cutoff, err := parseSince(opts.Since)
	if err != nil {
		return nil, err
	}

	var candidates []projectDirInfo
	for _, p := range projects {
		if !p.lastUsed.IsZero() && p.lastUsed.Before(cutoff) {
			candidates = append(candidates, p)
		}
	}

	if len(candidates) == 0 {
		return "No projects found", nil
	}

	options := make([]huh.Option[string], len(candidates))
	for i, p := range candidates {
		lastUsed := "never"
		if !p.lastUsed.IsZero() {
			lastUsed = claude.FormatTimeAgo(&p.lastUsed)
		}
		label := fmt.Sprintf("%-50s %3d sessions  %8s  %s",
			truncate(filepath.Base(p.name), 50),
			p.sessions, formatBytes(p.size), lastUsed)
		options[i] = huh.NewOption(label, p.dirName)
	}

	var selected []string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select projects to remove").
				Options(options...).
				Value(&selected).
				Height(20).
				Filterable(true),
		),
	)

	if err := form.Run(); err != nil {
		return nil, err
	}

	if len(selected) == 0 {
		return "No projects selected", nil
	}

	projectsDir := claude.GetProjectsDir()

	if opts.DryRun {
		return dryRunRemoveProjects(projectsDir, selected), nil
	}

	var totalSessions int
	var totalFreed int64
	for _, dirName := range selected {
		dirPath := filepath.Join(projectsDir, dirName)
		sessions, freed := removeProjectDir(dirPath)
		totalSessions += sessions
		totalFreed += freed
	}

	return ProjectsCleanResult{
		SessionsDeleted: totalSessions,
		SpaceFreed:      formatBytes(totalFreed),
	}, nil
}

func dryRunRemoveProjects(projectsDir string, selected []string) string {
	var b strings.Builder
	for _, dirName := range selected {
		fmt.Fprintf(&b, "rm -rf %s\n", shellQuote(filepath.Join(projectsDir, dirName)))
	}
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
