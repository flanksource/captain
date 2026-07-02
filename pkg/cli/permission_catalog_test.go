package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestBuildPermissionCatalog(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	mustWrite(t, filepath.Join(workspace, ".mcp.json"), `{"mcpServers":{"filesystem":{},"gavel":{}}}`)
	if err := os.Mkdir(filepath.Join(workspace, ".skills"), 0o755); err != nil {
		t.Fatalf("mkdir workspace .skills: %v", err)
	}
	mustWrite(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"ado":{},"gavel":{}}}`)
	if err := os.MkdirAll(filepath.Join(home, ".codex", "plugins", "captain"), 0o755); err != nil {
		t.Fatalf("mkdir codex plugin: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", "review"), 0o755); err != nil {
		t.Fatalf("mkdir claude skill: %v", err)
	}

	catalog := buildPermissionCatalog(workspace)

	if !hasCatalogID(catalog.Tools, "Read") || !hasCatalogID(catalog.Tools, "Bash") {
		t.Fatalf("tools missing builtins: %+v", catalog.Tools)
	}
	for _, want := range []string{"filesystem", "gavel", "ado"} {
		if !hasCatalogID(catalog.MCP, want) {
			t.Fatalf("mcp missing %q: %+v", want, catalog.MCP)
		}
	}
	if !hasCatalogID(catalog.Plugins, filepath.Join(home, ".codex", "plugins", "captain")) {
		t.Fatalf("plugins missing captain: %+v", catalog.Plugins)
	}
	if !hasCatalogID(catalog.Skills, "$CWD/.skills") {
		t.Fatalf("skills missing workspace .skills: %+v", catalog.Skills)
	}
	if !hasCatalogID(catalog.Skills, filepath.Join(home, ".claude", "skills", "review")) {
		t.Fatalf("skills missing claude skill: %+v", catalog.Skills)
	}
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func hasCatalogID(items []api.PermissionCatalogItem, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
