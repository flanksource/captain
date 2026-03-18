package container

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type DiscoverConfig struct {
	ClaudeDir      string
	ClaudeJSONPath string
	ContainerHome  string
	LocalDepPaths  []string
}

func DefaultDiscoverConfig() DiscoverConfig {
	home := os.Getenv("HOME")
	user := DetectHostUser()
	pwd, _ := os.Getwd()
	cfg := DiscoverConfig{
		ClaudeDir:     filepath.Join(home, ".claude"),
		ContainerHome: user.ContainerHome(),
	}
	if pwd != "" {
		cfg.LocalDepPaths = discoverLocalDeps(pwd)
	}
	return cfg
}

func (cfg DiscoverConfig) containerHome() string {
	if cfg.ContainerHome != "" {
		return cfg.ContainerHome
	}
	return "/home/node"
}

func resolveSymlink(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

func DiscoverAll(cfg DiscoverConfig) []Component {
	var all []Component
	all = append(all, discoverAgents(cfg)...)
	all = append(all, discoverSkills(cfg)...)
	all = append(all, discoverMCP(cfg)...)
	all = append(all, discoverPlugins(cfg)...)
	all = append(all, discoverCommands(cfg)...)
	all = append(all, discoverHooks(cfg)...)
	all = append(all, discoverSettingsGranular(cfg)...)
	all = append(all, discoverMCPServers(cfg)...)
	all = append(all, discoverAuth(cfg)...)
	all = append(all, discoverFeatureFlags(cfg)...)
	all = append(all, discoverProjectEntries(cfg)...)
	all = append(all, discoverClaudeMD(cfg)...)
	return all
}

func discoverAgents(cfg DiscoverConfig) []Component {
	dir := filepath.Join(cfg.ClaudeDir, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []Component
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		src := resolveSymlink(filepath.Join(dir, e.Name()))
		result = append(result, Component{
			Category:    CategoryAgents,
			Name:        name,
			SourcePath:  src,
			TargetPath:  cfg.containerHome() + "/.claude/agents/" + e.Name(),
			Description: extractFrontmatterField(src, "description"),
		})
	}
	return result
}

func discoverSkills(cfg DiscoverConfig) []Component {
	dir := filepath.Join(cfg.ClaudeDir, "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []Component
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		src := resolveSymlink(filepath.Join(dir, e.Name()))
		result = append(result, Component{
			Category:   CategorySkills,
			Name:       e.Name(),
			SourcePath: src,
			TargetPath: cfg.containerHome() + "/.claude/skills/" + e.Name(),
			IsDir:      true,
		})
	}
	return result
}

func discoverMCP(cfg DiscoverConfig) []Component {
	dir := filepath.Join(cfg.ClaudeDir, "mcp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []Component
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		src := resolveSymlink(filepath.Join(dir, e.Name()))
		result = append(result, Component{
			Category:   CategoryMCP,
			Name:       e.Name(),
			SourcePath: src,
			TargetPath: cfg.containerHome() + "/.claude/mcp/" + e.Name(),
			IsDir:      true,
		})
	}
	return result
}

func discoverPlugins(cfg DiscoverConfig) []Component {
	path := filepath.Join(cfg.ClaudeDir, "plugins", "installed_plugins.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	var result []Component
	for name := range raw.Plugins {
		result = append(result, Component{
			Category:   CategoryPlugins,
			Name:       name,
			SourcePath: path,
			TargetPath: cfg.containerHome() + "/.claude/plugins/installed_plugins.json",
		})
	}
	return result
}

func discoverCommands(cfg DiscoverConfig) []Component {
	dir := filepath.Join(cfg.ClaudeDir, "commands")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []Component
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		result = append(result, Component{
			Category:   CategoryCommands,
			Name:       name,
			SourcePath: resolveSymlink(filepath.Join(dir, e.Name())),
			TargetPath: cfg.containerHome() + "/.claude/commands/" + e.Name(),
		})
	}
	return result
}

func discoverHooks(cfg DiscoverConfig) []Component {
	dir := filepath.Join(cfg.ClaudeDir, "hooks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []Component
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		result = append(result, Component{
			Category:   CategoryHooks,
			Name:       e.Name(),
			SourcePath: resolveSymlink(filepath.Join(dir, e.Name())),
			TargetPath: cfg.containerHome() + "/.claude/hooks/" + e.Name(),
		})
	}
	return result
}

func extractFrontmatterField(path, field string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	inFrontmatter := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			if inFrontmatter {
				return ""
			}
			inFrontmatter = true
			continue
		}
		if inFrontmatter && strings.HasPrefix(line, field+":") {
			val := strings.TrimSpace(strings.TrimPrefix(line, field+":"))
			if len(val) > 80 {
				return val[:77] + "..."
			}
			return val
		}
	}
	return ""
}

func discoverClaudeMD(cfg DiscoverConfig) []Component {
	path := resolveSymlink(filepath.Join(cfg.ClaudeDir, "CLAUDE.md"))
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return []Component{{
		Category:        CategoryClaudeMD,
		Name:            "CLAUDE.md",
		SourcePath:      path,
		TargetPath:      cfg.containerHome() + "/.claude/CLAUDE.md",
		DefaultSelected: true,
	}}
}

func discoverLocalDeps(dir string) []string {
	var paths []string
	paths = append(paths, parseGoModReplace(filepath.Join(dir, "go.mod"), dir)...)
	paths = append(paths, parsePackageJSONFileDeps(filepath.Join(dir, "package.json"), dir)...)
	return paths
}

func parseGoModReplace(path, baseDir string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var paths []string
	lines := strings.Split(string(data), "\n")
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "replace (" {
			inBlock = true
			continue
		}
		if inBlock && trimmed == ")" {
			inBlock = false
			continue
		}
		var replacement string
		if inBlock {
			parts := strings.Fields(trimmed)
			if len(parts) >= 4 && parts[2] == "=>" {
				replacement = parts[3]
			} else if len(parts) >= 3 && parts[1] == "=>" {
				replacement = parts[2]
			}
		} else if strings.HasPrefix(trimmed, "replace ") {
			parts := strings.Fields(trimmed)
			idx := -1
			for i, p := range parts {
				if p == "=>" {
					idx = i
					break
				}
			}
			if idx >= 0 && idx+1 < len(parts) {
				replacement = parts[idx+1]
			}
		}
		if replacement == "" || (!strings.HasPrefix(replacement, "/") && !strings.HasPrefix(replacement, ".")) {
			continue
		}
		abs := replacement
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(baseDir, abs)
		}
		abs = filepath.Clean(abs)
		if root := FindGitRoot(abs); root != "" {
			paths = append(paths, root)
		}
	}
	return paths
}

func parsePackageJSONFileDeps(path, baseDir string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}

	var paths []string
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		for _, v := range deps {
			if !strings.HasPrefix(v, "file:") {
				continue
			}
			rel := strings.TrimPrefix(v, "file:")
			abs := filepath.Join(baseDir, rel)
			abs = filepath.Clean(abs)
			if root := FindGitRoot(abs); root != "" {
				paths = append(paths, root)
			}
		}
	}
	return paths
}

func FindGitRoot(path string) string {
	for p := path; p != "/" && p != "."; p = filepath.Dir(p) {
		gitPath := filepath.Join(p, ".git")
		info, err := os.Stat(gitPath)
		if err != nil {
			continue
		}
		if info.IsDir() || info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}
