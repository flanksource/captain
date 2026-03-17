package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateLaunchScriptCopyMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, ".config", "captain"), 0o755)

	path, err := GenerateLaunchScript(LaunchScriptInput{
		Name: "dev",
		Mode:    ModeCopy,
	})
	if err != nil {
		t.Fatalf("GenerateLaunchScript: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading script: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "claude-env:dev") {
		t.Error("missing image tag")
	}
	if !strings.Contains(content, "ANTHROPIC_API_KEY") {
		t.Error("missing API key passthrough")
	}
	if !strings.Contains(content, "#!/bin/bash") {
		t.Error("missing shebang")
	}
	if !strings.Contains(content, "-w \"$PWD\"") {
		t.Error("should use -w $PWD")
	}
	if !strings.Contains(content, "-v \"$PWD:$PWD\"") {
		t.Error("should use -v $PWD:$PWD")
	}

	info, _ := os.Stat(path)
	if info.Mode()&0o111 == 0 {
		t.Error("script not executable")
	}
}

func TestGenerateLaunchScriptMountMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, ".config", "captain"), 0o755)

	components := []Component{
		{Category: CategoryAgents, Name: "design", SourcePath: "/home/user/.claude/agents/design.md", TargetPath: "/home/node/.claude/agents/design.md", Selected: true},
		{Category: CategorySkills, Name: "copywriter", SourcePath: "/home/user/.dotfiles/claude/skills/copywriter", TargetPath: "/home/node/.claude/skills/copywriter", IsDir: true, Selected: true},
		{Category: CategoryAgents, Name: "unselected", SourcePath: "/tmp/x", TargetPath: "/tmp/y", Selected: false},
	}

	path, err := GenerateLaunchScript(LaunchScriptInput{
		Name:    "dev",
		Mode:       ModeMount,
		Components: components,
	})
	if err != nil {
		t.Fatalf("GenerateLaunchScript: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading script: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "-v") {
		t.Error("mount mode should have -v flags")
	}
	if !strings.Contains(content, "design.md") {
		t.Error("should mount design agent")
	}
	if !strings.Contains(content, "copywriter") {
		t.Error("should mount copywriter skill")
	}
	if strings.Contains(content, "unselected") {
		t.Error("unselected component should not be mounted")
	}
	if !strings.Contains(content, ":ro") {
		t.Error("mounts should be read-only")
	}
}
