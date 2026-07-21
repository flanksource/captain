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

func TestResolveCatalogDir(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "sub", "child")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	// Empty dir resolves to the workspace root.
	if got, err := resolveCatalogDir(base, ""); err != nil || got != filepath.Clean(base) {
		t.Fatalf("empty dir: got %q err %v, want %q", got, err, filepath.Clean(base))
	}

	// Relative paths that stay inside the workspace are allowed.
	if got, err := resolveCatalogDir(base, filepath.Join("sub", "child")); err != nil || got != nested {
		t.Fatalf("relative dir: got %q err %v, want %q", got, err, nested)
	}

	// Traversal attempts must be rejected.
	for _, dir := range []string{
		"../../etc",
		filepath.Join("sub", "..", "..", "etc"),
		"/etc",
	} {
		if got, err := resolveCatalogDir(base, dir); err == nil {
			t.Fatalf("expected %q to be rejected, got %q", dir, got)
		}
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
