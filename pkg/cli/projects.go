package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

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
