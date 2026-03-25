package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateShortcuts(t *testing.T) {
	dir := t.TempDir()
	cfg := SandboxConfig{
		Name:    "test-container",
		Image:   "claude-env:golang",
		Presets: []string{"golang"},
		User:    UserSpec{Username: "testuser", UID: 1000, GID: 1000},
	}
	buildArgs := BuildInput{
		Tag:        "claude-env:golang",
		ContextDir: filepath.Join(dir, ".container-sandbox"),
		User:       HostUser{Username: "testuser", UID: 1000, GID: 1000},
	}

	if err := GenerateShortcuts(ShortcutsInput{Dir: dir, Config: cfg, BuildArgs: buildArgs}); err != nil {
		t.Fatalf("GenerateShortcuts failed: %v", err)
	}

	scripts := []string{"sbx-build", "sbx-start", "sbx-exec", "sbx-run", "sbx-stop", "sbx-rm"}
	for _, name := range scripts {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("script %s not found: %v", name, err)
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("script %s is not executable", name)
		}
		content, _ := os.ReadFile(path)
		if !strings.HasPrefix(string(content), "#!/bin/bash") {
			t.Errorf("script %s missing shebang", name)
		}
	}
}

func TestShortcutBuildContent(t *testing.T) {
	dir := t.TempDir()
	input := ShortcutsInput{
		Dir: dir,
		Config: SandboxConfig{
			Name:  "mycontainer",
			Image: "claude-env:golang",
			User:  UserSpec{Username: "dev", UID: 501, GID: 20},
		},
		BuildArgs: BuildInput{
			Tag:  "claude-env:golang",
			User: HostUser{Username: "dev", UID: 501, GID: 20},
		},
		Components: []Component{
			{Category: CategoryAgents, Name: "design", SourcePath: "/home/dev/.claude/agents/design", Selected: true},
			{Category: CategoryHooks, Name: "pre-commit.sh", SourcePath: "/home/dev/.claude/hooks/pre-commit.sh", Selected: true},
			{Category: CategoryAgents, Name: "skills-dir", SourcePath: "/home/dev/.claude/agents/skills-dir", Selected: true, IsDir: true},
			{Category: CategorySettings, Name: "settings", ContentKey: "settings.json", Selected: true},
		},
	}
	GenerateShortcuts(input)

	content, _ := os.ReadFile(filepath.Join(dir, "sbx-build"))
	s := string(content)
	if !strings.Contains(s, "-t claude-env:golang") {
		t.Error("sbx-build should contain image tag")
	}
	if !strings.Contains(s, "USERNAME=dev") {
		t.Error("sbx-build should contain username build arg")
	}
	if !strings.Contains(s, "cp -f") {
		t.Error("sbx-build should contain cp commands for file components")
	}
	if !strings.Contains(s, "cp -rf") {
		t.Error("sbx-build should contain cp -rf for directory components")
	}
	if !strings.Contains(s, "trap") {
		t.Error("sbx-build should contain trap for cleanup")
	}
	if !strings.Contains(s, "rm -f") {
		t.Error("sbx-build should contain rm -f in cleanup")
	}
	if strings.Contains(s, "settings.json") {
		t.Error("sbx-build should skip ContentKey components")
	}
}

func TestShortcutStartContent(t *testing.T) {
	dir := t.TempDir()
	input := ShortcutsInput{
		Dir: dir,
		Config: SandboxConfig{
			Name:           "mycontainer",
			Image:          "claude-env:golang",
			User:           UserSpec{Username: "dev", UID: 501, GID: 20},
			EnvPassthrough: []string{"GOPATH"},
			Env:            map[string]string{"FOO": "bar"},
		},
		BuildArgs: BuildInput{Tag: "claude-env:golang", User: HostUser{Username: "dev", UID: 501, GID: 20}},
	}
	GenerateShortcuts(input)

	content, _ := os.ReadFile(filepath.Join(dir, "sbx-start"))
	s := string(content)
	if !strings.Contains(s, "docker run -d") {
		t.Error("sbx-start should contain docker run -d")
	}
	if !strings.Contains(s, "claude-env:golang") {
		t.Error("sbx-start should contain image name")
	}
	if !strings.Contains(s, "GOPATH") {
		t.Error("sbx-start should reference GOPATH passthrough")
	}
	if !strings.Contains(s, "FOO=bar") {
		t.Error("sbx-start should contain static env var")
	}
	if !strings.Contains(s, "ANTHROPIC_API_KEY") {
		t.Error("sbx-start should reference ANTHROPIC_API_KEY")
	}
}

func TestShortcutRunDelegates(t *testing.T) {
	dir := t.TempDir()
	input := ShortcutsInput{
		Dir: dir,
		Config: SandboxConfig{
			Name:  "mycontainer",
			Image: "claude-env:golang",
			User:  UserSpec{Username: "dev", UID: 501, GID: 20},
		},
		BuildArgs: BuildInput{Tag: "claude-env:golang", User: HostUser{Username: "dev", UID: 501, GID: 20}},
	}
	GenerateShortcuts(input)

	content, _ := os.ReadFile(filepath.Join(dir, "sbx-run"))
	s := string(content)
	if !strings.Contains(s, "sbx-start") {
		t.Error("sbx-run should delegate to sbx-start")
	}
	if !strings.Contains(s, "sbx-exec") {
		t.Error("sbx-run should delegate to sbx-exec")
	}
}

func TestShortcutExecContent(t *testing.T) {
	dir := t.TempDir()
	input := ShortcutsInput{
		Dir: dir,
		Config: SandboxConfig{
			Name:  "mycontainer",
			Image: "claude-env:golang",
			User:  UserSpec{Username: "dev", UID: 501, GID: 20},
		},
		BuildArgs: BuildInput{Tag: "claude-env:golang", User: HostUser{Username: "dev", UID: 501, GID: 20}},
	}
	GenerateShortcuts(input)

	content, _ := os.ReadFile(filepath.Join(dir, "sbx-exec"))
	s := string(content)
	if !strings.Contains(s, "docker exec -it") {
		t.Error("sbx-exec should contain docker exec -it")
	}
	if !strings.Contains(s, "bash") {
		t.Error("sbx-exec should default to bash")
	}
}

func TestShortcutDefaultName(t *testing.T) {
	dir := t.TempDir()
	input := ShortcutsInput{
		Dir: dir,
		Config: SandboxConfig{
			Image: "claude-env:golang",
			User:  UserSpec{Username: "dev", UID: 501, GID: 20},
		},
		BuildArgs: BuildInput{Tag: "claude-env:golang", User: HostUser{Username: "dev", UID: 501, GID: 20}},
	}
	GenerateShortcuts(input)

	content, _ := os.ReadFile(filepath.Join(dir, "sbx-stop"))
	s := string(content)
	// Should have a name derived from image + pwd
	if !strings.Contains(s, "docker stop") {
		t.Error("sbx-stop should contain docker stop")
	}
}
