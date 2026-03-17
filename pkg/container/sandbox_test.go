package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".container-sandbox.yaml")

	cfg := SandboxConfig{
		Image:     "claude-env:dev",
		Mode:      ModeCopy,
		BaseImage: "claude-env:base",
		Volumes: []Volume{
			{Source: "/home/user/.claude/agents/design.md", Target: "/home/user/.claude/agents/design.md", ReadOnly: true},
			{Source: "/home/user/project", Target: "/workspace/project"},
		},
		User:       UserSpec{Username: "moshe", UID: 501, GID: 20},
		Components: []string{"agents/design", "settings/Permissions (allow all)"},
		Options:    map[string]string{"permissions": "allow-all"},
	}

	if err := SaveSandboxConfig(path, cfg); err != nil {
		t.Fatalf("SaveSandboxConfig: %v", err)
	}

	loaded, err := LoadSandboxConfig(path)
	if err != nil {
		t.Fatalf("LoadSandboxConfig: %v", err)
	}

	if loaded.Image != cfg.Image {
		t.Errorf("Image: got %q, want %q", loaded.Image, cfg.Image)
	}
	if loaded.Mode != cfg.Mode {
		t.Errorf("Mode: got %q, want %q", loaded.Mode, cfg.Mode)
	}
	if loaded.User.Username != cfg.User.Username {
		t.Errorf("Username: got %q, want %q", loaded.User.Username, cfg.User.Username)
	}
	if loaded.User.UID != cfg.User.UID {
		t.Errorf("UID: got %d, want %d", loaded.User.UID, cfg.User.UID)
	}
	if len(loaded.Volumes) != 2 {
		t.Fatalf("Volumes: got %d, want 2", len(loaded.Volumes))
	}
	if !loaded.Volumes[0].ReadOnly {
		t.Error("first volume should be read-only")
	}
	if loaded.Volumes[1].ReadOnly {
		t.Error("second volume should not be read-only")
	}
}

func TestBuildSandboxConfigCopyMode(t *testing.T) {
	components := []Component{
		{Category: CategoryAgents, Name: "design", SourcePath: "/tmp/design.md", TargetPath: "/home/test/.claude/agents/design.md", Selected: true},
	}
	user := HostUser{Username: "test", UID: 1000, GID: 1000, HomeDir: "/home/test"}

	cfg := BuildSandboxConfig(ModeCopy, "claude-env:base", components, user, []string{"golang"})

	if cfg.Image != "claude-env:golang" {
		t.Errorf("Image: got %q, want %q", cfg.Image, "claude-env:golang")
	}
	if len(cfg.Volumes) != 0 {
		t.Errorf("copy mode should have no volumes, got %d", len(cfg.Volumes))
	}
	if len(cfg.Components) != 1 {
		t.Errorf("expected 1 component, got %d", len(cfg.Components))
	}
}

func TestBuildSandboxConfigNoPresets(t *testing.T) {
	components := []Component{
		{Category: CategoryAgents, Name: "design", SourcePath: "/tmp/design.md", TargetPath: "/home/test/.claude/agents/design.md", Selected: true},
	}
	user := HostUser{Username: "test", UID: 1000, GID: 1000, HomeDir: "/home/test"}

	cfg := BuildSandboxConfig(ModeCopy, "claude-env:base", components, user, nil)

	if cfg.Image != "claude-env:custom" {
		t.Errorf("Image: got %q, want %q", cfg.Image, "claude-env:custom")
	}
}

func TestBuildSandboxConfigMountMode(t *testing.T) {
	components := []Component{
		{Category: CategoryAgents, Name: "design", SourcePath: "/home/test/.claude/agents/design.md", TargetPath: "/home/test/.claude/agents/design.md", Selected: true},
		{Category: CategoryProjects, Name: "myproj", ContentKey: "projects./home/test/proj", GitRoot: "/home/test/proj", ProjectPath: "/home/test/.claude/projects/-home-test-proj", Selected: true},
		{Category: CategoryAgents, Name: "unselected", SourcePath: "/tmp/x", TargetPath: "/tmp/y", Selected: false},
	}
	user := HostUser{Username: "test", UID: 1000, GID: 1000, HomeDir: "/home/test"}

	cfg := BuildSandboxConfig(ModeMount, "claude-env:base", components, user, []string{"golang", "npm"})

	if cfg.Image != "claude-env:golang-npm" {
		t.Errorf("Image: got %q, want %q", cfg.Image, "claude-env:golang-npm")
	}
	if len(cfg.Volumes) != 3 {
		t.Fatalf("mount mode: got %d volumes, want 3 (agent + project workspace + project meta)", len(cfg.Volumes))
	}

	foundAgent := false
	foundWorkspace := false
	foundMeta := false
	for _, v := range cfg.Volumes {
		if v.Target == "/home/test/.claude/agents/design.md" && v.ReadOnly {
			foundAgent = true
		}
		if v.Target == "/workspace/proj" && !v.ReadOnly {
			foundWorkspace = true
		}
		if v.Target == "/home/test/.claude/projects/-home-test-proj" && v.ReadOnly {
			foundMeta = true
		}
	}
	if !foundAgent {
		t.Error("expected agent volume mount")
	}
	if !foundWorkspace {
		t.Error("expected project workspace volume mount")
	}
	if !foundMeta {
		t.Error("expected project metadata volume mount")
	}
}

func TestSandboxConfigYAMLFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")

	cfg := SandboxConfig{
		Image:   "claude-env:golang",
		Mode:    ModeMount,
		Presets: []string{"golang"},
		User:    UserSpec{Username: "moshe", UID: 501, GID: 20},
		Volumes: []Volume{
			{Source: "/Users/moshe/project", Target: "/workspace/project"},
		},
	}
	_ = SaveSandboxConfig(path, cfg)

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "image: claude-env:golang") {
		t.Error("expected image field in YAML")
	}
	if !strings.Contains(content, "mode: mount") {
		t.Error("expected mode field in YAML")
	}
	if !strings.Contains(content, "username: moshe") {
		t.Error("expected username in YAML")
	}
}

func TestExtractMCPEnvVars(t *testing.T) {
	dir := t.TempDir()
	claudeJSON := filepath.Join(dir, ".claude.json")
	_ = os.WriteFile(claudeJSON, []byte(`{
		"mcpServers": {
			"mission-control": {
				"type": "http",
				"url": "https://beta.flanksource.com/api/mcp",
				"headers": {"Authorization": "Basic ${BETA_MC_TOKEN}"},
				"env": {"TOKEN": "${BETA_MC_TOKEN}"}
			},
			"iconify": {
				"command": "npx",
				"args": ["-y", "iconify-mcp-server@latest"]
			},
			"multi-var": {
				"env": {"A": "${VAR_A}", "B": "${VAR_B}"}
			}
		}
	}`), 0o644)

	tests := []struct {
		name       string
		components []string
		envSetup   map[string]string
		wantVars   []string
		wantEmpty  bool
	}{
		{
			name:       "extracts env var from selected server",
			components: []string{"mcp-servers/mission-control"},
			envSetup:   map[string]string{"BETA_MC_TOKEN": "secret123"},
			wantVars:   []string{"BETA_MC_TOKEN=secret123"},
		},
		{
			name:       "no env vars for server without templates",
			components: []string{"mcp-servers/iconify"},
			wantEmpty:  true,
		},
		{
			name:       "skips unselected servers",
			components: []string{"mcp-servers/iconify"},
			envSetup:   map[string]string{"BETA_MC_TOKEN": "secret123"},
			wantEmpty:  true,
		},
		{
			name:       "skips unset env vars",
			components: []string{"mcp-servers/mission-control"},
			envSetup:   map[string]string{"BETA_MC_TOKEN": ""},
			wantEmpty:  true,
		},
		{
			name:       "deduplicates same var referenced multiple times",
			components: []string{"mcp-servers/mission-control"},
			envSetup:   map[string]string{"BETA_MC_TOKEN": "secret123"},
			wantVars:   []string{"BETA_MC_TOKEN=secret123"},
		},
		{
			name:       "multiple vars from one server",
			components: []string{"mcp-servers/multi-var"},
			envSetup:   map[string]string{"VAR_A": "aaa", "VAR_B": "bbb"},
			wantVars:   []string{"VAR_A=aaa", "VAR_B=bbb"},
		},
		{
			name:       "no mcp-servers components returns nil",
			components: []string{"agents/design"},
			wantEmpty:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envSetup {
				t.Setenv(k, v)
			}

			cfg := SandboxConfig{Components: tt.components}
			got := extractMCPEnvVars(cfg, claudeJSON)

			if tt.wantEmpty {
				if len(got) != 0 {
					t.Errorf("expected empty, got %v", got)
				}
				return
			}

			gotSet := make(map[string]bool)
			for _, v := range got {
				gotSet[v] = true
			}
			for _, want := range tt.wantVars {
				if !gotSet[want] {
					t.Errorf("missing expected env var %q in %v", want, got)
				}
			}
			if len(got) != len(tt.wantVars) {
				t.Errorf("got %d vars %v, want %d vars %v", len(got), got, len(tt.wantVars), tt.wantVars)
			}
		})
	}
}

func TestExtractMCPEnvVarsMissingFile(t *testing.T) {
	cfg := SandboxConfig{Components: []string{"mcp-servers/foo"}}
	got := extractMCPEnvVars(cfg, "/nonexistent/.claude.json")
	if len(got) != 0 {
		t.Errorf("expected empty for missing file, got %v", got)
	}
}
