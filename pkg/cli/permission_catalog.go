package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

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
		// The catalog describes one agent's world, so the runtime is required
		// rather than defaulted: there is no honest catalog for "some agent",
		// and the previous default — every source on the machine, merged — is
		// exactly the answer this endpoint no longer gives.
		provider, known := api.ProviderByName(strings.TrimSpace(r.URL.Query().Get("provider")))
		if !known {
			http.Error(w, fmt.Sprintf("provider must be one of: %s", api.ProviderList()), http.StatusBadRequest)
			return
		}
		mode, ok := api.ParseRuntimeMode(strings.TrimSpace(r.URL.Query().Get("mode")))
		if !ok {
			http.Error(w, fmt.Sprintf("mode must be one of: %s", api.RuntimeModeList()), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(buildPermissionCatalog(resolved, provider, mode)); err != nil {
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
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
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
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("resolve dir %q: %w", dir, err)
	}

	if resolvedTarget != resolvedBase && !strings.HasPrefix(resolvedTarget, resolvedBase+string(os.PathSeparator)) {
		return "", fmt.Errorf("dir %q escapes workspace root", dir)
	}
	return target, nil
}

// buildPermissionCatalog lists what the agent behind a runtime can actually see:
// its own built-in tools, and the MCP servers, skills and plugins it discovers
// from its own configuration. The API mode runs no agent CLI and therefore has
// an empty catalog rather than a borrowed one.
func buildPermissionCatalog(dir string, provider *api.ModelProvider, mode api.RuntimeMode) api.PermissionCatalog {
	home, _ := os.UserHomeDir()

	catalog := api.PermissionCatalog{Tools: catalogTools(provider, mode)}
	byKind := map[api.ResourceKind]map[string]api.PermissionCatalogItem{}
	for _, source := range api.AgentResourceSourcesFor(provider, mode) {
		root := dir
		if source.Scope == api.AgentScopeHome {
			if home == "" {
				continue
			}
			root = home
		}
		items, ok := byKind[source.Kind]
		if !ok {
			items = map[string]api.PermissionCatalogItem{}
			byKind[source.Kind] = items
		}
		readCatalogSource(items, filepath.Join(root, source.Path), source)
	}

	catalog.MCP = sortedCatalogItems(byKind[api.ResourceKindMCP])
	catalog.Skills = sortedCatalogItems(byKind[api.ResourceKindSkills])
	catalog.Plugins = sortedCatalogItems(byKind[api.ResourceKindPlugins])
	return catalog
}

// catalogTools projects the selected agent's declared tool vocabulary. The
// names used to be a hardcoded claude list served to codex and gemini runs,
// which could not name a single one of them.
func catalogTools(provider *api.ModelProvider, mode api.RuntimeMode) []api.PermissionCatalogItem {
	tools := api.AgentToolsFor(provider, mode)
	out := make([]api.PermissionCatalogItem, 0, len(tools))
	for _, tool := range tools {
		out = append(out, api.PermissionCatalogItem{
			ID:          tool.Name,
			Label:       tool.Name,
			Group:       tool.Group,
			Description: tool.Description,
			Source:      "builtin",
			Available:   true,
			DefaultMode: string(tool.Default),
		})
	}
	return out
}

// readCatalogSource adds every item one declared source yields. A source that
// does not exist, or cannot be parsed, contributes nothing: an agent's config
// files are the user's, and a missing ~/.codex/config.toml is the normal state
// of a machine without codex rather than a captain failure.
func readCatalogSource(out map[string]api.PermissionCatalogItem, path string, source api.AgentResourceSource) {
	switch source.Format {
	case api.SourceFormatMCPJSON:
		addNamedJSONEntries(out, path, source, "mcpServers")
	case api.SourceFormatPluginsJSON:
		addNamedJSONEntries(out, path, source, "plugins")
	case api.SourceFormatMCPTOML:
		addTOMLMCPServers(out, path, source)
	case api.SourceFormatChildDirs:
		addChildDirs(out, path, source)
	case api.SourceFormatDirectory:
		addDirectory(out, path, source)
	}
}

func addNamedJSONEntries(out map[string]api.PermissionCatalogItem, path string, source api.AgentResourceSource, key string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw[key], &entries); err != nil {
		return
	}
	for name := range entries {
		addCatalogItem(out, catalogItem(name, name, path, source))
	}
}

// addTOMLMCPServers reads codex's [mcp_servers.<name>] tables — the one source
// no other agent shares, and the one captain never read at all.
func addTOMLMCPServers(out map[string]api.PermissionCatalogItem, path string, source api.AgentResourceSource) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var servers struct {
		MCPServers map[string]map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(data, &servers); err != nil {
		return
	}
	for name := range servers.MCPServers {
		addCatalogItem(out, catalogItem(name, name, path, source))
	}
}

func addChildDirs(out map[string]api.PermissionCatalogItem, dir string, source api.AgentResourceSource) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		addCatalogItem(out, catalogItem(path, entry.Name(), path, source))
	}
}

func addDirectory(out map[string]api.PermissionCatalogItem, dir string, source api.AgentResourceSource) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return
	}
	id := source.ID
	if id == "" {
		id = dir
	}
	addCatalogItem(out, catalogItem(id, id, dir, source))
}

func catalogItem(id, label, path string, source api.AgentResourceSource) api.PermissionCatalogItem {
	return api.PermissionCatalogItem{
		ID:          id,
		Label:       label,
		Group:       source.Group,
		Source:      source.Source,
		SourcePath:  path,
		Configured:  true,
		Available:   true,
		DefaultMode: string(api.ResourceEnabled),
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
