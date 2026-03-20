package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStageSettingsAllowAll(t *testing.T) {
	dir := t.TempDir()

	settings := map[string]any{
		"$schema":     "https://json.schemastore.org/claude-code-settings.json",
		"permissions": map[string]any{"allow": []string{"Bash(ls:*)"}},
		"hooks":       map[string]any{"PreToolUse": []any{}},
		"sandbox":     map[string]any{"mode": "strict"},
	}
	data, _ := json.Marshal(settings)
	settingsPath := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(settingsPath, data, 0o644)

	contextDir := filepath.Join(dir, "build-context")
	_ = os.MkdirAll(contextDir, 0o755)

	components := []Component{
		{Category: CategorySettings, ContentKey: "permissions", OptionValue: "allow-all", Selected: true},
		{Category: CategorySettings, ContentKey: "hooks", Selected: true},
		{Category: CategorySettings, ContentKey: "sandbox", OptionValue: "off", Selected: true},
	}

	block, err := stageSettingsJSON(contextDir, settingsPath, components, "/home/test/.claude/settings.json")
	if err != nil {
		t.Fatalf("stageSettingsJSON: %v", err)
	}
	if block.Instruction == "" {
		t.Fatal("expected non-empty COPY instruction")
	}

	outData, _ := os.ReadFile(filepath.Join(contextDir, "settings.json"))
	var result map[string]json.RawMessage
	_ = json.Unmarshal(outData, &result)

	var perms map[string]any
	_ = json.Unmarshal(result["permissions"], &perms)
	allowList, ok := perms["allow"].([]any)
	if !ok || len(allowList) < 5 {
		t.Errorf("expected expanded allow-all permissions, got %v", perms)
	}
	foundBash := false
	for _, v := range allowList {
		if v == "Bash(*)" {
			foundBash = true
		}
	}
	if !foundBash {
		t.Errorf("expected Bash(*) in allow list, got %v", allowList)
	}

	if _, ok := result["hooks"]; !ok {
		t.Error("expected hooks key")
	}
	if _, ok := result["sandbox"]; ok {
		t.Error("sandbox should be omitted when option is 'off'")
	}
}

func TestStageSettingsKeep(t *testing.T) {
	dir := t.TempDir()

	settings := map[string]any{
		"permissions": map[string]any{"allow": []string{"Bash(ls:*)"}},
		"sandbox":     map[string]any{"mode": "strict"},
	}
	data, _ := json.Marshal(settings)
	settingsPath := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(settingsPath, data, 0o644)

	contextDir := filepath.Join(dir, "build-context")
	_ = os.MkdirAll(contextDir, 0o755)

	components := []Component{
		{Category: CategorySettings, ContentKey: "permissions", OptionValue: "keep", Selected: true},
		{Category: CategorySettings, ContentKey: "sandbox", OptionValue: "keep", Selected: true},
	}

	_, err := stageSettingsJSON(contextDir, settingsPath, components, "/home/test/.claude/settings.json")
	if err != nil {
		t.Fatalf("stageSettingsJSON: %v", err)
	}

	outData, _ := os.ReadFile(filepath.Join(contextDir, "settings.json"))
	var result map[string]json.RawMessage
	_ = json.Unmarshal(outData, &result)

	var perms map[string]any
	_ = json.Unmarshal(result["permissions"], &perms)
	allowList, ok := perms["allow"].([]any)
	if !ok || len(allowList) != 1 || allowList[0] != "Bash(ls:*)" {
		t.Errorf("expected original permissions, got %v", perms)
	}

	if _, ok := result["sandbox"]; !ok {
		t.Error("sandbox should be preserved when option is 'keep'")
	}
}

func TestStageClaudeJSONFilteredServers(t *testing.T) {
	dir := t.TempDir()

	claudeJSON := map[string]any{
		"hasCompletedOnboarding":        true,
		"shiftEnterKeyBindingInstalled": true,
		"mcpServers": map[string]any{
			"iconify": map[string]any{"command": "npx"},
			"lucide":  map[string]any{"command": "npx"},
			"gemini":  map[string]any{"command": "npx"},
		},
		"oauthAccount": map[string]any{"token": "abc"},
	}
	data, _ := json.Marshal(claudeJSON)
	srcPath := filepath.Join(dir, "claude.json")
	_ = os.WriteFile(srcPath, data, 0o644)

	contextDir := filepath.Join(dir, "build-context")
	_ = os.MkdirAll(contextDir, 0o755)

	components := []Component{
		{Category: CategoryMCPServers, ContentKey: "mcpServers.iconify", Selected: true},
		{Category: CategoryMCPServers, ContentKey: "mcpServers.lucide", Selected: true},
		{Category: CategoryMCPServers, ContentKey: "mcpServers.gemini", Selected: false},
		{Category: CategoryAuth, ContentKey: "oauthAccount", Selected: true},
	}

	block, err := stageClaudeJSON(contextDir, srcPath, components, "/home/test/.claude.json")
	if err != nil {
		t.Fatalf("stageClaudeJSON: %v", err)
	}
	if block.Instruction == "" {
		t.Fatal("expected non-empty COPY instruction")
	}

	outData, _ := os.ReadFile(filepath.Join(contextDir, "claude.json"))
	var result map[string]json.RawMessage
	_ = json.Unmarshal(outData, &result)

	if _, ok := result["hasCompletedOnboarding"]; !ok {
		t.Error("expected base key hasCompletedOnboarding")
	}
	if _, ok := result["oauthAccount"]; !ok {
		t.Error("expected oauthAccount")
	}

	var servers map[string]json.RawMessage
	_ = json.Unmarshal(result["mcpServers"], &servers)
	if len(servers) != 2 {
		t.Errorf("got %d servers, want 2 (iconify, lucide)", len(servers))
	}
	if _, ok := servers["gemini"]; ok {
		t.Error("gemini should not be included (not selected)")
	}
}

func TestStageClaudeJSONWithProjects(t *testing.T) {
	dir := t.TempDir()

	projPath := filepath.Join(dir, "myproject")
	_ = os.MkdirAll(filepath.Join(projPath, ".git"), 0o755)

	claudeJSON := map[string]any{
		"hasCompletedOnboarding": true,
		"projects": map[string]any{
			projPath: map[string]any{"allowedTools": []string{}},
		},
		"githubRepoPaths": map[string][]string{
			"test/myproject": {projPath},
		},
	}
	data, _ := json.Marshal(claudeJSON)
	srcPath := filepath.Join(dir, "claude.json")
	_ = os.WriteFile(srcPath, data, 0o644)

	components := []Component{
		{Category: CategoryProjects, ContentKey: "projects." + projPath, GitRoot: projPath, Selected: true},
	}

	contextDir := filepath.Join(dir, "build-context")
	_ = os.MkdirAll(contextDir, 0o755)

	_, err := stageClaudeJSON(contextDir, srcPath, components, "/home/test/.claude.json")
	if err != nil {
		t.Fatalf("stageClaudeJSON: %v", err)
	}

	outData, _ := os.ReadFile(filepath.Join(contextDir, "claude.json"))
	var result map[string]json.RawMessage
	_ = json.Unmarshal(outData, &result)

	if _, ok := result["projects"]; !ok {
		t.Error("expected projects in output")
	}
	if _, ok := result["githubRepoPaths"]; !ok {
		t.Error("expected githubRepoPaths in output for selected project")
	}

	var grp map[string][]string
	_ = json.Unmarshal(result["githubRepoPaths"], &grp)
	if paths, ok := grp["test/myproject"]; !ok || len(paths) == 0 {
		t.Errorf("expected test/myproject in githubRepoPaths, got %v", grp)
	}
}
