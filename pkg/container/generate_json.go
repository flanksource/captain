package container

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var settingsPreferenceKeys = []string{
	"includeCoAuthoredBy", "effortLevel", "alwaysThinkingEnabled",
	"skipDangerousModePermissionPrompt", "autoUpdatesChannel",
}

var claudeJSONPassthroughKeys = []string{
	"hasCompletedOnboarding", "shiftEnterKeyBindingInstalled",
}

var allowAllPermissions = []string{
	"Bash(*)",
	"Read(*)",
	"Edit(*)",
	"Write(*)",
	"Glob",
	"Grep",
	"WebFetch",
	"WebSearch",
	"mcp__*",
}

func stageSettingsJSON(contextDir string, settingsPath string, components []Component, targetPath string) (copyBlock, error) {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return copyBlock{}, fmt.Errorf("reading settings.json: %w", err)
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(data, &full); err != nil {
		return copyBlock{}, fmt.Errorf("parsing settings.json: %w", err)
	}

	partial := make(map[string]json.RawMessage)
	if v, ok := full["$schema"]; ok {
		partial["$schema"] = v
	}

	settingsComps := FilterByCategory(components, CategorySettings)
	selectedKeys := make(map[string]string)
	for _, c := range settingsComps {
		if c.Selected && c.ContentKey != "" {
			selectedKeys[c.ContentKey] = c.OptionValue
		}
	}

	if opt, ok := selectedKeys["permissions"]; ok {
		if opt == "allow-all" {
			allowAll := map[string]any{"allow": allowAllPermissions}
			d, _ := json.Marshal(allowAll)
			partial["permissions"] = d
		} else if v, ok := full["permissions"]; ok {
			partial["permissions"] = v
		}
	}

	if opt, ok := selectedKeys["sandbox"]; ok && opt != "off" {
		if v, ok := full["sandbox"]; ok {
			partial["sandbox"] = v
		}
	}

	for _, key := range []string{"hooks", "statusLine", "enabledPlugins"} {
		if _, ok := selectedKeys[key]; ok {
			if v, ok := full[key]; ok {
				partial[key] = v
			}
		}
	}

	if _, ok := selectedKeys["preferences"]; ok {
		for _, pk := range settingsPreferenceKeys {
			if v, ok := full[pk]; ok {
				partial[pk] = v
			}
		}
	}

	return writePartialJSON(contextDir, "settings.json", partial, targetPath, "settings.json")
}

func stageClaudeJSON(contextDir string, claudeJSONPath string, components []Component, targetPath string) (copyBlock, error) {
	data, err := os.ReadFile(claudeJSONPath)
	if err != nil {
		return copyBlock{}, fmt.Errorf("reading .claude.json: %w", err)
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(data, &full); err != nil {
		return copyBlock{}, fmt.Errorf("parsing .claude.json: %w", err)
	}

	partial := make(map[string]json.RawMessage)
	for _, bk := range claudeJSONPassthroughKeys {
		if v, ok := full[bk]; ok {
			partial[bk] = v
		}
	}

	authComps := FilterByCategory(components, CategoryAuth)
	for _, c := range authComps {
		if c.Selected {
			if v, ok := full["oauthAccount"]; ok {
				partial["oauthAccount"] = v
			}
			break
		}
	}

	mcpComps := FilterByCategory(components, CategoryMCPServers)
	var allServers map[string]json.RawMessage
	if raw, ok := full["mcpServers"]; ok {
		_ = json.Unmarshal(raw, &allServers)
	}
	selectedServers := make(map[string]json.RawMessage)
	for _, c := range mcpComps {
		if !c.Selected {
			continue
		}
		name := strings.TrimPrefix(c.ContentKey, "mcpServers.")
		if v, ok := allServers[name]; ok {
			selectedServers[name] = v
		}
	}
	if len(selectedServers) > 0 {
		d, _ := json.Marshal(selectedServers)
		partial["mcpServers"] = d
	}

	projComps := FilterByCategory(components, CategoryProjects)
	var allProjects map[string]json.RawMessage
	if raw, ok := full["projects"]; ok {
		_ = json.Unmarshal(raw, &allProjects)
	}
	selectedProjects := make(map[string]json.RawMessage)
	var selectedGitRoots []string
	for _, c := range projComps {
		if !c.Selected {
			continue
		}
		projPath := strings.TrimPrefix(c.ContentKey, "projects.")
		if v, ok := allProjects[projPath]; ok {
			selectedProjects[projPath] = v
		}
		if c.GitRoot != "" {
			selectedGitRoots = append(selectedGitRoots, c.GitRoot)
		}
	}
	if len(selectedProjects) > 0 {
		d, _ := json.Marshal(selectedProjects)
		partial["projects"] = d
	}
	if len(selectedGitRoots) > 0 {
		grp := buildGithubRepoPaths(full, selectedGitRoots)
		if len(grp) > 0 {
			d, _ := json.Marshal(grp)
			partial["githubRepoPaths"] = d
		}
	}

	return writePartialJSON(contextDir, "claude.json", partial, targetPath, ".claude.json")
}

func buildGithubRepoPaths(full map[string]json.RawMessage, selectedRoots []string) map[string][]string {
	rootSet := make(map[string]bool, len(selectedRoots))
	for _, r := range selectedRoots {
		rootSet[r] = true
	}

	var allGRP map[string][]string
	if raw, ok := full["githubRepoPaths"]; ok {
		_ = json.Unmarshal(raw, &allGRP)
	}

	result := make(map[string][]string)
	for repo, paths := range allGRP {
		for _, p := range paths {
			include := false
			if rootSet[p] {
				include = true
			} else {
				for root := range rootSet {
					if strings.HasPrefix(p, root+"/") {
						include = true
						break
					}
				}
			}
			if include {
				result[repo] = append(result[repo], p)
			}
		}
	}
	return result
}

var patchTargetFiles = map[string]string{
	"settings.json": "settings.json",
	"claude.json":   "claude.json",
}

func applyPatches(contextDir string, patches []Patch) error {
	for _, p := range patches {
		filename, ok := patchTargetFiles[p.Target]
		if !ok {
			return fmt.Errorf("unknown patch target %q", p.Target)
		}
		filePath := filepath.Join(contextDir, filename)
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("reading %s for patching: %w", filename, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parsing %s for patching: %w", filename, err)
		}

		if p.StrategicMerge != nil {
			strategicMerge(doc, p.StrategicMerge)
		}
		for _, op := range p.JSONPatch {
			if err := applyJSONPatchOp(doc, op); err != nil {
				return fmt.Errorf("applying jsonPatch to %s: %w", filename, err)
			}
		}

		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling patched %s: %w", filename, err)
		}
		if err := os.WriteFile(filePath, out, 0o644); err != nil {
			return fmt.Errorf("writing patched %s: %w", filename, err)
		}
	}
	return nil
}

func strategicMerge(dst, src map[string]any) {
	for k, v := range src {
		if srcMap, ok := v.(map[string]any); ok {
			if dstMap, ok := dst[k].(map[string]any); ok {
				strategicMerge(dstMap, srcMap)
				continue
			}
		}
		dst[k] = v
	}
}

func applyJSONPatchOp(doc map[string]any, op JSONPatchOp) error {
	parts := splitJSONPointer(op.Path)
	if len(parts) == 0 {
		return fmt.Errorf("empty path in jsonPatch op")
	}
	parent, leaf := parts[:len(parts)-1], parts[len(parts)-1]

	var target any = doc
	for _, p := range parent {
		m, ok := target.(map[string]any)
		if !ok {
			return fmt.Errorf("path segment %q: not an object", p)
		}
		target = m[p]
	}

	m, ok := target.(map[string]any)
	if !ok {
		return fmt.Errorf("parent of %q is not an object", op.Path)
	}

	switch op.Op {
	case "add", "replace":
		m[leaf] = op.Value
	case "remove":
		delete(m, leaf)
	default:
		return fmt.Errorf("unsupported jsonPatch op %q", op.Op)
	}
	return nil
}

func splitJSONPointer(path string) []string {
	if path == "" || path == "/" {
		return nil
	}
	trimmed := strings.TrimPrefix(path, "/")
	return strings.Split(trimmed, "/")
}

func writePartialJSON(contextDir, filename string, partial map[string]json.RawMessage, target, comment string) (copyBlock, error) {
	data, err := json.MarshalIndent(partial, "", "  ")
	if err != nil {
		return copyBlock{}, fmt.Errorf("marshaling %s: %w", filename, err)
	}

	destPath := filepath.Join(contextDir, filename)
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return copyBlock{}, fmt.Errorf("writing %s: %w", filename, err)
	}

	return copyBlock{
		Comment:     comment,
		Instruction: fmt.Sprintf("COPY %s %s", filename, target),
	}, nil
}
