package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/timberio/go-datemath"
)

type ProjectsListOptions struct{}

type ProjectRow struct {
	Project  string `json:"project" pretty:"label=Project,table"`
	Sessions int    `json:"sessions" pretty:"label=Sessions,table"`
	Size     string `json:"size" pretty:"label=Size,table"`
	LastUsed string `json:"lastUsed" pretty:"label=Last Used,table"`
}

type ProjectsListResult struct {
	Total int          `json:"total" pretty:"label=Total Projects"`
	Rows  []ProjectRow `json:"rows"`
}

type ProjectOption struct {
	Value    string     `json:"value"`
	Label    string     `json:"label"`
	Path     string     `json:"path"`
	Sources  []string   `json:"sources,omitempty"`
	Sessions int        `json:"sessions,omitempty"`
	LastUsed *time.Time `json:"lastUsed,omitempty"`
}

type ProjectOptionsResult struct {
	Total    int             `json:"total"`
	Projects []ProjectOption `json:"projects"`
}

type projectOptionAccumulator struct {
	path     string
	sources  map[string]bool
	sessions int
	lastUsed time.Time
}

func RunProjectsList(_ ProjectsListOptions) (any, error) {
	projects, err := scanProjects()
	if err != nil {
		return nil, err
	}

	rows := make([]ProjectRow, len(projects))
	for i, p := range projects {
		var lastUsed string
		if !p.lastUsed.IsZero() {
			lastUsed = claude.FormatTimeAgo(&p.lastUsed)
		}
		rows[i] = ProjectRow{
			Project:  p.name,
			Sessions: p.sessions,
			Size:     formatBytes(p.size),
			LastUsed: lastUsed,
		}
	}

	return ProjectsListResult{Total: len(projects), Rows: rows}, nil
}

func RunProjectOptions() (ProjectOptionsResult, error) {
	accs := map[string]*projectOptionAccumulator{}

	projects, err := scanProjects()
	if err != nil {
		return ProjectOptionsResult{}, err
	}
	for _, project := range projects {
		addProjectOption(accs, projectOptionPath(project), "claude", project.sessions, project.lastUsed)
	}

	if codexFiles, err := history.FindCodexSessionFiles(); err == nil {
		for _, file := range codexFiles {
			meta, err := history.ReadCodexSessionMeta(file)
			if err != nil || meta == nil || strings.TrimSpace(meta.CWD) == "" {
				continue
			}
			lastUsed := time.Time{}
			if info, err := os.Stat(file); err == nil {
				lastUsed = info.ModTime()
			}
			if meta.StartedAt != nil && meta.StartedAt.After(lastUsed) {
				lastUsed = *meta.StartedAt
			}
			addProjectOption(accs, sessionProjectRoot(meta.CWD), "codex", 1, lastUsed)
		}
	}

	if processes, err := discoverSessionProcesses(); err == nil {
		for _, proc := range processes {
			if strings.TrimSpace(proc.CWD) == "" {
				continue
			}
			lastUsed := time.Time{}
			if proc.StartedAt != nil {
				lastUsed = *proc.StartedAt
			}
			addProjectOption(accs, sessionProjectRoot(proc.CWD), "live", 0, lastUsed)
		}
	}

	projectsOut := make([]ProjectOption, 0, len(accs))
	for _, acc := range accs {
		sources := make([]string, 0, len(acc.sources))
		for source := range acc.sources {
			sources = append(sources, source)
		}
		sort.Strings(sources)
		var lastUsed *time.Time
		if !acc.lastUsed.IsZero() {
			value := acc.lastUsed
			lastUsed = &value
		}
		projectsOut = append(projectsOut, ProjectOption{
			Value:    acc.path,
			Label:    projectOptionLabel(acc.path),
			Path:     acc.path,
			Sources:  sources,
			Sessions: acc.sessions,
			LastUsed: lastUsed,
		})
	}

	sort.Slice(projectsOut, func(i, j int) bool {
		left, right := projectsOut[i], projectsOut[j]
		if left.LastUsed != nil && right.LastUsed != nil && !left.LastUsed.Equal(*right.LastUsed) {
			return left.LastUsed.After(*right.LastUsed)
		}
		if left.LastUsed != nil && right.LastUsed == nil {
			return true
		}
		if left.LastUsed == nil && right.LastUsed != nil {
			return false
		}
		return left.Label < right.Label
	})

	return ProjectOptionsResult{Total: len(projectsOut), Projects: projectsOut}, nil
}

func addProjectOption(accs map[string]*projectOptionAccumulator, path, source string, sessions int, lastUsed time.Time) {
	path = normalizeSessionProject(path)
	if path == "" {
		return
	}
	acc := accs[path]
	if acc == nil {
		acc = &projectOptionAccumulator{path: path, sources: map[string]bool{}}
		accs[path] = acc
	}
	if source != "" {
		acc.sources[source] = true
	}
	acc.sessions += sessions
	if lastUsed.After(acc.lastUsed) {
		acc.lastUsed = lastUsed
	}
}

func projectOptionPath(project projectDirInfo) string {
	sessions, _ := filepath.Glob(filepath.Join(project.dirPath, "*.jsonl"))
	sort.Slice(sessions, func(i, j int) bool {
		left, leftErr := os.Stat(sessions[i])
		right, rightErr := os.Stat(sessions[j])
		if leftErr != nil || rightErr != nil {
			return sessions[i] < sessions[j]
		}
		return left.ModTime().After(right.ModTime())
	})
	for _, sessionFile := range sessions {
		entries, err := claude.ReadHistoryFileWithOptions(sessionFile, claude.ReadOptions{})
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if strings.TrimSpace(entry.CWD) != "" {
				return sessionProjectRoot(entry.CWD)
			}
		}
	}
	return project.name
}

func projectOptionLabel(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) >= 2 {
		return strings.Join(filtered[len(filtered)-2:], "/")
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return path
}

type ProjectsCleanOptions struct {
	Since       string `flag:"since" help:"Delete sessions not accessed within this period (e.g. 30d, now-30d)" default:"30d" short:"s"`
	DryRun      bool   `flag:"dry-run" help:"Preview without deleting" short:"n"`
	Interactive bool   `flag:"interactive" help:"Interactively select projects to remove" short:"i"`
}

var bareDurationRe = regexp.MustCompile(`^\d+[smhdwMy]$`)

func parseSince(s string) (time.Time, error) {
	if bareDurationRe.MatchString(s) {
		s = "now-" + s
	}
	expr, err := datemath.Parse(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid since value %q: %w", s, err)
	}
	return expr.Time(datemath.WithNow(time.Now())), nil
}

type CleanRow struct {
	Project string `json:"project" pretty:"label=Project,table"`
	File    string `json:"file" pretty:"label=File,table"`
	Size    string `json:"size" pretty:"label=Size,table"`
	LastMod string `json:"lastMod" pretty:"label=Last Modified,table"`
}

type ProjectsCleanResult struct {
	DryRun          bool       `json:"dryRun" pretty:"label=Dry Run"`
	SessionsDeleted int        `json:"sessionsDeleted" pretty:"label=Sessions Deleted"`
	SpaceFreed      string     `json:"spaceFreed" pretty:"label=Space Freed"`
	Rows            []CleanRow `json:"rows"`
}

type projectDirInfo struct {
	dirName  string
	dirPath  string
	name     string
	sessions int
	size     int64
	lastUsed time.Time
}

func scanProjects() ([]projectDirInfo, error) {
	projectsDir := claude.GetProjectsDir()
	if projectsDir == "" || !filepath.IsAbs(projectsDir) {
		return nil, fmt.Errorf("could not determine Claude projects directory")
	}
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var projects []projectDirInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath := filepath.Join(projectsDir, entry.Name())
		sessions, _ := filepath.Glob(filepath.Join(dirPath, "*.jsonl"))

		var lastMod time.Time
		for _, s := range sessions {
			if fi, err := os.Stat(s); err == nil && fi.ModTime().After(lastMod) {
				lastMod = fi.ModTime()
			}
		}

		projects = append(projects, projectDirInfo{
			dirName:  entry.Name(),
			dirPath:  dirPath,
			name:     claude.DenormalizePath(entry.Name()),
			sessions: len(sessions),
			size:     dirSize(dirPath),
			lastUsed: lastMod,
		})
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].lastUsed.After(projects[j].lastUsed)
	})
	return projects, nil
}

func removeProjectDir(dirPath string) (int, int64) {
	sessions, _ := filepath.Glob(filepath.Join(dirPath, "*.jsonl"))
	var totalFreed int64
	for _, f := range sessions {
		if fi, err := os.Stat(f); err == nil {
			totalFreed += fi.Size()
		}
		stem := uuidStem(f)
		if stem != "" {
			uuidDir := filepath.Join(dirPath, stem)
			if di, err := os.Stat(uuidDir); err == nil && di.IsDir() {
				totalFreed += dirSize(uuidDir)
				_ = os.RemoveAll(uuidDir)
			}
		}
		_ = os.Remove(f)
	}
	remaining, _ := os.ReadDir(dirPath)
	if len(remaining) == 0 {
		_ = os.Remove(dirPath)
	}
	return len(sessions), totalFreed
}

func uuidStem(jsonlPath string) string {
	base := filepath.Base(jsonlPath)
	stem := base[:len(base)-len(filepath.Ext(base))]
	if stem == "" || stem == "." || stem == ".." {
		return ""
	}
	return stem
}

func RunProjectsClean(opts ProjectsCleanOptions) (any, error) {
	if opts.Interactive {
		return runProjectsCleanInteractive(opts)
	}

	cutoff, err := parseSince(opts.Since)
	if err != nil {
		return nil, err
	}

	projects, err := scanProjects()
	if err != nil {
		return nil, err
	}

	var rows []CleanRow
	var totalFreed int64
	deleted := 0

	for _, p := range projects {
		sessions, _ := filepath.Glob(filepath.Join(p.dirPath, "*.jsonl"))
		for _, sessionFile := range sessions {
			fi, err := os.Stat(sessionFile)
			if err != nil {
				continue
			}
			if fi.ModTime().After(cutoff) {
				continue
			}

			sessionSize := fi.Size()
			base := filepath.Base(sessionFile)
			stem := uuidStem(sessionFile)
			if stem != "" {
				uuidDir := filepath.Join(p.dirPath, stem)
				if di, err := os.Stat(uuidDir); err == nil && di.IsDir() {
					sessionSize += dirSize(uuidDir)
					if !opts.DryRun {
						_ = os.RemoveAll(uuidDir)
					}
				}
			}

			modTime := fi.ModTime()
			rows = append(rows, CleanRow{
				Project: p.name,
				File:    base,
				Size:    formatBytes(sessionSize),
				LastMod: claude.FormatTimeAgo(&modTime),
			})

			if !opts.DryRun {
				_ = os.Remove(sessionFile)
			}
			totalFreed += sessionSize
			deleted++
		}

		if !opts.DryRun {
			remaining, _ := os.ReadDir(p.dirPath)
			if len(remaining) == 0 {
				_ = os.Remove(p.dirPath)
			}
		}
	}

	return ProjectsCleanResult{
		DryRun:          opts.DryRun,
		SessionsDeleted: deleted,
		SpaceFreed:      formatBytes(totalFreed),
		Rows:            rows,
	}, nil
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func dirSize(path string) int64 {
	var size int64
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			size += fi.Size()
		}
		return nil
	})
	return size
}
