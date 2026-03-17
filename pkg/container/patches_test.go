package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchesStrategicMerge(t *testing.T) {
	dir := t.TempDir()
	original := map[string]any{
		"permissions": map[string]any{"allow": []string{"Bash(*)"}},
		"hooks":       map[string]any{"PreToolUse": []any{}},
	}
	data, _ := json.MarshalIndent(original, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644)

	patches := []Patch{{
		Target: "settings.json",
		StrategicMerge: map[string]any{
			"permissions": map[string]any{
				"allow": []string{"Bash(*)", "Read(*)", "Edit(*)"},
			},
		},
	}}

	if err := applyPatches(dir, patches); err != nil {
		t.Fatalf("applyPatches: %v", err)
	}

	out, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	var result map[string]any
	_ = json.Unmarshal(out, &result)

	perms, ok := result["permissions"].(map[string]any)
	if !ok {
		t.Fatal("permissions not a map")
	}
	allow, ok := perms["allow"].([]any)
	if !ok || len(allow) != 3 {
		t.Errorf("expected 3 allow entries, got %v", perms["allow"])
	}
	if _, ok := result["hooks"]; !ok {
		t.Error("hooks should be preserved")
	}
}

func TestApplyPatchesJSONPatch(t *testing.T) {
	dir := t.TempDir()
	original := map[string]any{
		"hasCompletedOnboarding": true,
		"mcpServers":             map[string]any{"iconify": map[string]any{}},
	}
	data, _ := json.MarshalIndent(original, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "claude.json"), data, 0o644)

	patches := []Patch{{
		Target: "claude.json",
		JSONPatch: []JSONPatchOp{
			{Op: "add", Path: "/customKey", Value: "customValue"},
			{Op: "remove", Path: "/mcpServers"},
		},
	}}

	if err := applyPatches(dir, patches); err != nil {
		t.Fatalf("applyPatches: %v", err)
	}

	out, _ := os.ReadFile(filepath.Join(dir, "claude.json"))
	var result map[string]any
	_ = json.Unmarshal(out, &result)

	if result["customKey"] != "customValue" {
		t.Errorf("expected customKey=customValue, got %v", result["customKey"])
	}
	if _, ok := result["mcpServers"]; ok {
		t.Error("mcpServers should have been removed")
	}
	if result["hasCompletedOnboarding"] != true {
		t.Error("hasCompletedOnboarding should be preserved")
	}
}

func TestApplyPatchesUnknownTarget(t *testing.T) {
	dir := t.TempDir()
	patches := []Patch{{Target: "unknown.json"}}
	if err := applyPatches(dir, patches); err == nil {
		t.Error("expected error for unknown target")
	}
}

func TestApplySelections(t *testing.T) {
	components := []Component{
		{Category: CategoryAgents, Name: "design", ContentKey: "", OptionValue: ""},
		{Category: CategorySettings, Name: "Permissions (allow all)", ContentKey: "permissions", OptionValue: "keep"},
		{Category: CategorySettings, Name: "Sandbox (off)", ContentKey: "sandbox", OptionValue: "keep"},
	}

	selected := []string{"agents/design", "settings/Permissions (allow all)"}
	options := map[string]string{"permissions": "allow-all"}

	ApplySelections(components, selected, options)

	if !components[0].Selected {
		t.Error("agents/design should be selected")
	}
	if !components[1].Selected {
		t.Error("settings/Permissions should be selected")
	}
	if components[1].OptionValue != "allow-all" {
		t.Errorf("expected OptionValue allow-all, got %q", components[1].OptionValue)
	}
	if components[2].Selected {
		t.Error("sandbox should not be selected")
	}
}

func TestSandboxConfigRoundTripWithComponents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")

	cfg := SandboxConfig{
		Image:      "claude-env:dev",
		Mode:       ModeCopy,
		BaseImage:  "claude-env:base",
		User:       UserSpec{Username: "test", UID: 1000, GID: 1000},
		Components: []string{"agents/design", "settings/Permissions (allow all)"},
		Options:    map[string]string{"permissions": "allow-all", "sandbox": "off"},
		Patches: []Patch{{
			Target:         "settings.json",
			StrategicMerge: map[string]any{"permissions": map[string]any{"allow": []string{"Bash(*)"}}},
		}},
	}

	if err := SaveSandboxConfig(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadSandboxConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(loaded.Components) != 2 {
		t.Errorf("expected 2 components, got %d", len(loaded.Components))
	}
	if loaded.Options["permissions"] != "allow-all" {
		t.Errorf("expected permissions=allow-all, got %q", loaded.Options["permissions"])
	}
	if len(loaded.Patches) != 1 {
		t.Errorf("expected 1 patch, got %d", len(loaded.Patches))
	}
	if loaded.Patches[0].Target != "settings.json" {
		t.Errorf("expected patch target settings.json, got %q", loaded.Patches[0].Target)
	}
}
