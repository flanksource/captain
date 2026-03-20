package container

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func discoverProjectEntries(cfg DiscoverConfig) []Component {
	path := ClaudeJSONPath(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	projectDirs := listProjectDirs(cfg)

	var result []Component
	result = append(result, projectsFromProjectDirs(path, raw, projectDirs, cfg)...)
	result = append(result, projectsFromGithubRepoPaths(path, raw, projectDirs, cfg)...)

	result = deduplicateProjects(result)
	sortProjectsByLastAccess(result)
	return result
}

func projectsFromProjectDirs(path string, raw map[string]json.RawMessage, projectDirs map[string]string, cfg DiscoverConfig) []Component {
	projectsRaw, ok := raw["projects"]
	if !ok {
		return nil
	}
	var projects map[string]json.RawMessage
	if err := json.Unmarshal(projectsRaw, &projects); err != nil {
		return nil
	}

	var result []Component
	for projPath := range projects {
		encoded := encodeProjectPath(projPath)
		metaDir, ok := projectDirs[encoded]
		if !ok {
			continue
		}
		gitRoot := FindGitRoot(projPath)
		if gitRoot == "" {
			continue
		}
		lastAccess := projectLastAccess(metaDir)
		result = append(result, Component{
			Category:    CategoryProjects,
			Name:        projectDisplayName(projPath),
			SourcePath:  path,
			TargetPath:  cfg.containerHome() + "/.claude.json",
			ContentKey:  "projects." + projPath,
			ProjectPath: metaDir,
			GitRoot:     gitRoot,
			LastAccess:  lastAccess,
		})
	}
	return result
}

func projectsFromGithubRepoPaths(path string, raw map[string]json.RawMessage, projectDirs map[string]string, cfg DiscoverConfig) []Component {
	grpRaw, ok := raw["githubRepoPaths"]
	if !ok {
		return nil
	}
	var grp map[string][]string
	if err := json.Unmarshal(grpRaw, &grp); err != nil {
		return nil
	}

	var result []Component
	for _, paths := range grp {
		for _, p := range paths {
			gitRoot := FindGitRoot(p)
			if gitRoot == "" {
				continue
			}
			encoded := encodeProjectPath(gitRoot)
			metaDir := projectDirs[encoded]
			var lastAccess string
			if metaDir != "" {
				lastAccess = projectLastAccess(metaDir)
			}

			result = append(result, Component{
				Category:    CategoryProjects,
				Name:        projectDisplayName(gitRoot),
				SourcePath:  path,
				TargetPath:  cfg.containerHome() + "/.claude.json",
				ContentKey:  "projects." + gitRoot,
				ProjectPath: metaDir,
				GitRoot:     gitRoot,
				LastAccess:  lastAccess,
			})
		}
	}
	return result
}

func listProjectDirs(cfg DiscoverConfig) map[string]string {
	dir := filepath.Join(cfg.ClaudeDir, "projects")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	result := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			result[e.Name()] = filepath.Join(dir, e.Name())
		}
	}
	return result
}

func deduplicateProjects(components []Component) []Component {
	seen := make(map[string]int)
	var result []Component
	for _, c := range components {
		if c.GitRoot == "" {
			result = append(result, c)
			continue
		}
		if idx, ok := seen[c.GitRoot]; ok {
			if c.LastAccess > result[idx].LastAccess {
				result[idx] = c
			}
			continue
		}
		seen[c.GitRoot] = len(result)
		result = append(result, c)
	}
	return result
}

func sortProjectsByLastAccess(components []Component) {
	sort.SliceStable(components, func(i, j int) bool {
		if components[i].LastAccess == "" {
			return false
		}
		if components[j].LastAccess == "" {
			return true
		}
		return components[i].LastAccess > components[j].LastAccess
	})
}

func projectLastAccess(metaDir string) string {
	entries, err := os.ReadDir(metaDir)
	if err != nil {
		return ""
	}
	var latest time.Time
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		ts := lastTimestampInJSONL(filepath.Join(metaDir, e.Name()))
		if ts.After(latest) {
			latest = ts
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.Format(time.RFC3339)
}

func lastTimestampInJSONL(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close() //nolint:errcheck

	var lastLine string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		lastLine = scanner.Text()
	}
	if lastLine == "" {
		return time.Time{}
	}
	var entry struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal([]byte(lastLine), &entry); err != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
	return t
}

func projectDisplayName(path string) string {
	home := os.Getenv("HOME")
	if home != "" {
		path = strings.TrimPrefix(path, home+"/")
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "github.com" && i+2 < len(parts) {
			return strings.Join(parts[i+1:], "/")
		}
	}
	if len(parts) > 3 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return strings.Join(parts, "/")
}

func encodeProjectPath(path string) string {
	return strings.ReplaceAll(path, "/", "-")
}
