package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

func handlePermissionCatalog(baseCwd string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dir := strings.TrimSpace(r.URL.Query().Get("dir"))
		if dir == "" {
			dir = strings.TrimSpace(r.URL.Query().Get("cwd"))
		}
		resolved, err := resolveCatalogDir(baseCwd, dir)
		if err != nil {
			http.Error(w, "invalid dir", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(buildPermissionCatalog(resolved)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// resolveCatalogDir resolves the caller-supplied directory against baseCwd and
// guarantees the result stays within baseCwd. The dir value comes straight from
// an untrusted query parameter, so it must be confined to the workspace root to
// prevent path traversal (e.g. "../../etc") into arbitrary parts of the
// filesystem.
func resolveCatalogDir(baseCwd, dir string) (string, error) {
	base, err := filepath.Abs(baseCwd)
	if err != nil {
		return "", err
	}
	base = filepath.Clean(base)

	if dir == "" {
		return base, nil
	}

	// Reject traversal sequences in the raw input before any path is built;
	// the prefix check below is defense in depth.
	if strings.Contains(dir, "..") {
		return "", fmt.Errorf("dir %q contains a path traversal sequence", dir)
	}

	target := dir
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	target = filepath.Clean(target)

	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("dir %q escapes workspace root", dir)
	}
	return target, nil
}

func buildPermissionCatalog(dir string) api.PermissionCatalog {
	home, _ := os.UserHomeDir()
	catalog := api.PermissionCatalog{
		Tools: builtinPermissionTools(),
	}

	mcp := map[string]api.PermissionCatalogItem{}
	addMCPServers(mcp, filepath.Join(dir, ".mcp.json"), "workspace")
	if home != "" {
		addMCPServers(mcp, filepath.Join(home, ".claude.json"), "claude")
	}
	catalog.MCP = sortedCatalogItems(mcp)

	plugins := map[string]api.PermissionCatalogItem{}
	if home != "" {
		addPluginDirs(plugins, filepath.Join(home, ".codex", "plugins"), "codex")
		addClaudeInstalledPlugins(plugins, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	}
	catalog.Plugins = sortedCatalogItems(plugins)

	skills := map[string]api.PermissionCatalogItem{}
	addWorkspaceSkills(skills, dir)
	if home != "" {
		addSkillDirs(skills, filepath.Join(home, ".claude", "skills"), "claude")
		addSkillDirs(skills, filepath.Join(home, ".agents", "skills"), "agents")
	}
	catalog.Skills = sortedCatalogItems(skills)

	return catalog
}

func builtinPermissionTools() []api.PermissionCatalogItem {
	return []api.PermissionCatalogItem{
		{ID: "Read", Label: "Read", Group: "Files", Description: "Read files from the workspace.", Source: "builtin", Available: true, DefaultMode: "auto"},
		{ID: "Edit", Label: "Edit", Group: "Files", Description: "Apply targeted file edits.", Source: "builtin", Available: true, DefaultMode: "auto"},
		{ID: "Write", Label: "Write", Group: "Files", Description: "Write a new file.", Source: "builtin", Available: true, DefaultMode: "auto"},
		{ID: "MultiEdit", Label: "MultiEdit", Group: "Files", Description: "Apply several edits to one file.", Source: "builtin", Available: true, DefaultMode: "auto"},
		{ID: "Glob", Label: "Glob", Group: "Files", Description: "Find files by glob pattern.", Source: "builtin", Available: true, DefaultMode: "auto"},
		{ID: "Grep", Label: "Grep", Group: "Files", Description: "Search file contents.", Source: "builtin", Available: true, DefaultMode: "auto"},
		{ID: "Bash", Label: "Bash", Group: "Shell", Description: "Run shell commands.", Source: "builtin", Available: true, DefaultMode: "ask"},
		{ID: "WebSearch", Label: "WebSearch", Group: "Web", Description: "Search the web.", Source: "builtin", Available: true, DefaultMode: "ask"},
		{ID: "WebFetch", Label: "WebFetch", Group: "Web", Description: "Fetch a web page.", Source: "builtin", Available: true, DefaultMode: "ask"},
		{ID: "TodoWrite", Label: "TodoWrite", Group: "Planning", Description: "Track task progress.", Source: "builtin", Available: true, DefaultMode: "auto"},
	}
}

func addMCPServers(out map[string]api.PermissionCatalogItem, path, source string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for name := range raw.MCPServers {
		addCatalogItem(out, api.PermissionCatalogItem{
			ID:          name,
			Label:       name,
			Group:       "MCP",
			Source:      source,
			SourcePath:  path,
			Configured:  true,
			Available:   true,
			DefaultMode: "enabled",
		})
	}
}

func addPluginDirs(out map[string]api.PermissionCatalogItem, dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		addCatalogItem(out, api.PermissionCatalogItem{
			ID:          path,
			Label:       entry.Name(),
			Group:       "Plugins",
			Source:      source,
			SourcePath:  path,
			Configured:  true,
			Available:   true,
			DefaultMode: "enabled",
		})
	}
}

func addClaudeInstalledPlugins(out map[string]api.PermissionCatalogItem, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for name := range raw.Plugins {
		addCatalogItem(out, api.PermissionCatalogItem{
			ID:          name,
			Label:       name,
			Group:       "Plugins",
			Source:      "claude",
			SourcePath:  path,
			Configured:  true,
			Available:   true,
			DefaultMode: "enabled",
		})
	}
}

func addWorkspaceSkills(out map[string]api.PermissionCatalogItem, dir string) {
	path := filepath.Join(dir, ".skills")
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		addCatalogItem(out, api.PermissionCatalogItem{
			ID:          "$CWD/.skills",
			Label:       "$CWD/.skills",
			Group:       "Skills",
			Source:      "workspace",
			SourcePath:  path,
			Configured:  true,
			Available:   true,
			DefaultMode: "enabled",
		})
	}
}

func addSkillDirs(out map[string]api.PermissionCatalogItem, dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		addCatalogItem(out, api.PermissionCatalogItem{
			ID:          path,
			Label:       entry.Name(),
			Group:       "Skills",
			Source:      source,
			SourcePath:  path,
			Configured:  true,
			Available:   true,
			DefaultMode: "enabled",
		})
	}
}

func addCatalogItem(out map[string]api.PermissionCatalogItem, item api.PermissionCatalogItem) {
	if strings.TrimSpace(item.ID) == "" {
		return
	}
	if existing, ok := out[item.ID]; ok {
		if existing.SourcePath == "" {
			existing.SourcePath = item.SourcePath
		}
		if existing.Source == "" {
			existing.Source = item.Source
		}
		existing.Configured = existing.Configured || item.Configured
		existing.Available = existing.Available || item.Available
		out[item.ID] = existing
		return
	}
	out[item.ID] = item
}

func sortedCatalogItems(items map[string]api.PermissionCatalogItem) []api.PermissionCatalogItem {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]api.PermissionCatalogItem, 0, len(keys))
	for _, key := range keys {
		out = append(out, items[key])
	}
	return out
}
