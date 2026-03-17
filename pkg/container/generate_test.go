package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	_ = os.MkdirAll(srcDir, 0o755)

	agentFile := filepath.Join(srcDir, "design.md")
	_ = os.WriteFile(agentFile, []byte("# design agent"), 0o644)

	hookFile := filepath.Join(srcDir, "notify.sh")
	_ = os.WriteFile(hookFile, []byte("#!/bin/bash\necho hi"), 0o644)

	components := []Component{
		{Category: CategoryAgents, Name: "design", SourcePath: agentFile, TargetPath: "/home/node/.claude/agents/design.md", Selected: true},
		{Category: CategoryHooks, Name: "notify.sh", SourcePath: hookFile, TargetPath: "/home/node/.claude/hooks/notify.sh", Selected: true},
		{Category: CategoryAgents, Name: "unselected", SourcePath: agentFile, TargetPath: "/home/node/.claude/agents/unselected.md", Selected: false},
	}

	outDir := filepath.Join(dir, "out")
	_ = os.MkdirAll(outDir, 0o755)

	contextDir, err := Generate(GenerateInput{
		Name:    "test",
		BaseImage:  "mybase:latest",
		Components: components,
		OutputDir:  outDir,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	dockerfilePath := filepath.Join(contextDir, "Dockerfile")
	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("reading generated dockerfile: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "FROM mybase:latest") {
		t.Error("missing FROM instruction")
	}
	if !strings.Contains(content, "COPY agents/design /home/node/.claude/agents/design.md") {
		t.Error("missing agent COPY")
	}
	if !strings.Contains(content, "COPY hooks/notify.sh /home/node/.claude/hooks/notify.sh") {
		t.Error("missing hook COPY")
	}
	if !strings.Contains(content, "chmod +x") {
		t.Error("missing chmod for hooks")
	}
	if strings.Contains(content, "unselected") {
		t.Error("unselected component should not appear")
	}

	if _, err := os.Stat(filepath.Join(contextDir, "agents", "design")); err != nil {
		t.Error("agent file not staged in build context")
	}
	if _, err := os.Stat(filepath.Join(contextDir, "hooks", "notify.sh")); err != nil {
		t.Error("hook file not staged in build context")
	}
	if _, err := os.Stat(filepath.Join(contextDir, ".gitignore")); err != nil {
		t.Error(".gitignore not created in context dir")
	}
}

func TestGenerateMountMode(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	_ = os.MkdirAll(outDir, 0o755)

	components := []Component{
		{Category: CategoryAgents, Name: "design", SourcePath: "/tmp/x", TargetPath: "/home/node/.claude/agents/design.md", Selected: true},
	}

	contextDir, err := Generate(GenerateInput{
		Name:    "mounttest",
		BaseImage:  "mybase:latest",
		Mode:       ModeMount,
		Components: components,
		OutputDir:  outDir,
	})
	if err != nil {
		t.Fatalf("Generate mount: %v", err)
	}

	dockerfilePath := filepath.Join(contextDir, "Dockerfile")
	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("reading generated dockerfile: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "mount mode") {
		t.Error("should indicate mount mode")
	}
	if !strings.Contains(content, "mkdir -p") {
		t.Error("should create mount-point directories")
	}
	if strings.Contains(content, "COPY") {
		t.Error("mount mode should not have COPY instructions")
	}
}

func TestGenerateNoSelection(t *testing.T) {
	dir := t.TempDir()
	components := []Component{
		{Category: CategoryAgents, Name: "a", Selected: false},
	}
	_, err := Generate(GenerateInput{
		Name:    "empty",
		Components: components,
		OutputDir:  dir,
	})
	if err == nil {
		t.Error("expected error for no selection")
	}
}
