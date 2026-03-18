package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAgents(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".claude", "agents")
	_ = os.MkdirAll(agentDir, 0o755)

	_ = os.WriteFile(filepath.Join(agentDir, "test-agent.md"), []byte(`---
name: test-agent
description: A test agent
---
Instructions here
`), 0o644)

	cfg := DiscoverConfig{
		ClaudeDir: filepath.Join(dir, ".claude"),
	}
	components := DiscoverAll(cfg)

	agents := FilterByCategory(components, CategoryAgents)
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	if agents[0].Name != "test-agent" {
		t.Errorf("name: got %q, want %q", agents[0].Name, "test-agent")
	}
	if agents[0].Description != "A test agent" {
		t.Errorf("description: got %q, want %q", agents[0].Description, "A test agent")
	}
}

func TestDiscoverSkillsSkipsHidden(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, ".claude", "skills")
	_ = os.MkdirAll(filepath.Join(skillsDir, ".claude"), 0o755)
	_ = os.MkdirAll(filepath.Join(skillsDir, "real-skill"), 0o755)

	cfg := DiscoverConfig{
		ClaudeDir: filepath.Join(dir, ".claude"),
	}
	components := DiscoverAll(cfg)
	skills := FilterByCategory(components, CategorySkills)
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
	if skills[0].Name != "real-skill" {
		t.Errorf("name: got %q, want %q", skills[0].Name, "real-skill")
	}
}

func TestDiscoverSettingsGranular(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	_ = os.MkdirAll(claudeDir, 0o755)

	settings := map[string]interface{}{
		"permissions": map[string]interface{}{"allow": []string{"Bash(ls:*)"}},
		"hooks":       map[string]interface{}{"PreToolUse": []interface{}{}},
		"statusLine":  "enabled",
	}
	data, _ := json.Marshal(settings)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644)

	cfg := DiscoverConfig{
		ClaudeDir: claudeDir,
	}
	components := DiscoverAll(cfg)
	settingsComps := FilterByCategory(components, CategorySettings)
	if len(settingsComps) != 4 {
		t.Fatalf("got %d settings, want 4 (permissions allow-all, permissions keep, hooks, statusLine): %v", len(settingsComps), settingsComps)
	}

	foundAllowAll := false
	foundKeep := false
	for _, c := range settingsComps {
		if c.ContentKey == "permissions" && c.OptionValue == "allow-all" {
			foundAllowAll = true
		}
		if c.ContentKey == "permissions" && c.OptionValue == "keep" {
			foundKeep = true
		}
	}
	if !foundAllowAll {
		t.Error("expected Permissions (allow all) component")
	}
	if !foundKeep {
		t.Error("expected Permissions (keep) component")
	}
}

func TestDiscoverClaudeMD(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	_ = os.MkdirAll(claudeDir, 0o755)
	_ = os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("# test"), 0o644)

	cfg := DiscoverConfig{
		ClaudeDir: claudeDir,
	}
	components := DiscoverAll(cfg)
	md := FilterByCategory(components, CategoryClaudeMD)
	if len(md) != 1 {
		t.Fatalf("got %d claude-md, want 1", len(md))
	}
}

func TestDiscoverCommands(t *testing.T) {
	dir := t.TempDir()
	cmdDir := filepath.Join(dir, ".claude", "commands")
	_ = os.MkdirAll(cmdDir, 0o755)
	_ = os.WriteFile(filepath.Join(cmdDir, "test.md"), []byte("test"), 0o644)

	cfg := DiscoverConfig{
		ClaudeDir: filepath.Join(dir, ".claude"),
	}
	components := DiscoverAll(cfg)
	cmds := FilterByCategory(components, CategoryCommands)
	if len(cmds) != 1 {
		t.Fatalf("got %d commands, want 1", len(cmds))
	}
}

func TestDiscoverMCPServers(t *testing.T) {
	dir := t.TempDir()
	claudeJSON := filepath.Join(dir, ".claude.json")
	data, _ := json.Marshal(map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"iconify": map[string]interface{}{"command": "npx", "args": []string{"iconify-mcp"}},
			"lucide":  map[string]interface{}{"command": "npx", "args": []string{"lucide-mcp"}},
		},
	})
	_ = os.WriteFile(claudeJSON, data, 0o644)

	cfg := DiscoverConfig{
		ClaudeDir:      filepath.Join(dir, ".claude"),
		ClaudeJSONPath: claudeJSON,
	}
	components := DiscoverAll(cfg)
	servers := FilterByCategory(components, CategoryMCPServers)
	if len(servers) != 2 {
		t.Fatalf("got %d mcp-servers, want 2", len(servers))
	}

	foundIconify := false
	for _, s := range servers {
		if s.Name == "iconify" && s.ContentKey == "mcpServers.iconify" {
			foundIconify = true
		}
	}
	if !foundIconify {
		t.Error("expected iconify MCP server component")
	}
}

func TestDiscoverProjects(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")

	projPath := filepath.Join(dir, "myproject")
	_ = os.MkdirAll(filepath.Join(projPath, ".git"), 0o755)

	encoded := encodeProjectPath(projPath)
	metaDir := filepath.Join(claudeDir, "projects", encoded)
	_ = os.MkdirAll(metaDir, 0o755)

	claudeJSON := filepath.Join(dir, ".claude.json")
	data, _ := json.Marshal(map[string]any{
		"projects": map[string]any{
			projPath: map[string]any{"allowedTools": []string{}},
		},
	})
	_ = os.WriteFile(claudeJSON, data, 0o644)

	cfg := DiscoverConfig{
		ClaudeDir:      claudeDir,
		ClaudeJSONPath: claudeJSON,
	}
	components := DiscoverAll(cfg)
	projects := FilterByCategory(components, CategoryProjects)
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}
	if projects[0].ProjectPath != metaDir {
		t.Errorf("ProjectPath: got %q, want %q", projects[0].ProjectPath, metaDir)
	}
	if projects[0].GitRoot != projPath {
		t.Errorf("GitRoot: got %q, want %q", projects[0].GitRoot, projPath)
	}
}

func TestParseGoModReplace(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "localmod")
	_ = os.MkdirAll(filepath.Join(projDir, ".git"), 0o755)

	gomod := filepath.Join(dir, "go.mod")
	_ = os.WriteFile(gomod, []byte(`module example.com/mymod

go 1.21

replace (
	github.com/foo/bar => `+projDir+`
	github.com/baz/qux => ../nonexistent
)

replace github.com/single/line => `+projDir+`
`), 0o644)

	paths := parseGoModReplace(gomod, dir)
	if len(paths) < 1 {
		t.Fatalf("got %d paths, want at least 1", len(paths))
	}
	found := false
	for _, p := range paths {
		if p == projDir {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q in paths, got %v", projDir, paths)
	}
}

func TestParsePackageJSONFileDeps(t *testing.T) {
	dir := t.TempDir()
	depDir := filepath.Join(dir, "localpkg")
	_ = os.MkdirAll(filepath.Join(depDir, ".git"), 0o755)

	pkgJSON := filepath.Join(dir, "package.json")
	data, _ := json.Marshal(map[string]any{
		"dependencies": map[string]string{
			"my-local": "file:./localpkg",
			"lodash":   "^4.0.0",
		},
	})
	_ = os.WriteFile(pkgJSON, data, 0o644)

	paths := parsePackageJSONFileDeps(pkgJSON, dir)
	if len(paths) != 1 {
		t.Fatalf("got %d paths, want 1", len(paths))
	}
	if paths[0] != depDir {
		t.Errorf("got %q, want %q", paths[0], depDir)
	}
}

func TestApplyDefaultsAutoSelectsCWDProject(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)

	components := []Component{
		{Category: CategoryProjects, Name: "other", GitRoot: "/tmp/other"},
		{Category: CategoryProjects, Name: "cwd-project", GitRoot: dir},
		{Category: CategorySkills, Name: "some-skill"},
	}

	origDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(origDir) }()

	ApplyDefaults(components)
	if components[0].Selected {
		t.Error("other project should not be selected")
	}
	if !components[1].Selected {
		t.Error("CWD project should be auto-selected")
	}
	if !components[2].Selected {
		t.Error("skills should be auto-selected")
	}
}

func TestApplyDefaultsAutoSelectsLocalDeps(t *testing.T) {
	depDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(depDir, ".git"), 0o755)

	components := []Component{
		{Category: CategoryProjects, Name: "dep-project", GitRoot: depDir},
		{Category: CategoryProjects, Name: "other", GitRoot: "/tmp/other"},
	}

	ApplyDefaults(components, []string{depDir})
	if !components[0].Selected {
		t.Error("local dep project should be auto-selected")
	}
	if components[1].Selected {
		t.Error("other project should not be selected")
	}
}

func TestProjectDisplayName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/Users/moshe/go/src/github.com/flanksource/captain", "flanksource/captain"},
		{"/Users/moshe/.dotfiles", ".dotfiles"},
	}
	for _, tt := range tests {
		got := projectDisplayName(tt.path)
		if got != tt.want {
			t.Errorf("projectDisplayName(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
