package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
)

func TestAnalyzeToolUse_Read(t *testing.T) {
	a := AnalyzeToolUse(claude.ToolUse{
		Tool:  "Read",
		Input: map[string]any{"file_path": "/home/user/project/src/main.go"},
	}, "/home/user/project")

	assert.Equal(t, []string{"src/main.go"}, a.ReadPaths)
	assert.Empty(t, a.WritePaths)
	assert.Empty(t, a.Binaries)
	assert.Empty(t, a.Domains)
}

func TestAnalyzeToolUse_Grep(t *testing.T) {
	a := AnalyzeToolUse(claude.ToolUse{
		Tool:  "Grep",
		Input: map[string]any{"pattern": "TODO", "path": "/home/user/project/pkg/"},
	}, "/home/user/project")

	assert.Equal(t, []string{"pkg/"}, a.ReadPaths)
	assert.Empty(t, a.WritePaths)
}

func TestAnalyzeToolUse_Glob(t *testing.T) {
	a := AnalyzeToolUse(claude.ToolUse{
		Tool:  "Glob",
		Input: map[string]any{"pattern": "**/*.go", "path": "/home/user/project/pkg"},
	}, "/home/user/project")

	assert.Equal(t, []string{"pkg"}, a.ReadPaths)
	assert.Empty(t, a.WritePaths)
}

func TestAnalyzeToolUse_Write(t *testing.T) {
	a := AnalyzeToolUse(claude.ToolUse{
		Tool:  "Write",
		Input: map[string]any{"file_path": "/home/user/project/pkg/cli/new.go", "content": "package cli"},
	}, "/home/user/project")

	assert.Empty(t, a.ReadPaths)
	assert.Equal(t, []string{"pkg/cli/new.go"}, a.WritePaths)
}

func TestAnalyzeToolUse_Edit(t *testing.T) {
	a := AnalyzeToolUse(claude.ToolUse{
		Tool:  "Edit",
		Input: map[string]any{"file_path": "/home/user/project/cmd/main.go"},
	}, "/home/user/project")

	assert.Empty(t, a.ReadPaths)
	assert.Equal(t, []string{"cmd/main.go"}, a.WritePaths)
}

func TestAnalyzeToolUse_Bash(t *testing.T) {
	tests := []struct {
		name       string
		cmd        string
		readPaths  []string
		writePaths []string
		binaries   []string
		domains    []string
	}{
		{
			name:     "go build",
			cmd:      "go build ./...",
			binaries: []string{"go"},
			domains:  []string{},
		},
		{
			name:       "touch creates file",
			cmd:        "touch /home/user/project/output.txt",
			readPaths:  []string{"output.txt"},
			writePaths: []string{"output.txt"},
			binaries:   []string{"touch"},
			domains:    []string{},
		},
		{
			name:     "git with github domain",
			cmd:      "gh pr create --title test",
			binaries: []string{"gh"},
			domains:  []string{"*.github.com", "api.github.com", "github.com"},
		},
		{
			name:     "npm install with registry",
			cmd:      "npm install express",
			binaries: []string{"npm"},
			domains:  []string{"*.npmjs.org", "registry.npmjs.org"},
		},
		{
			name:     "curl with url domain",
			cmd:      "curl https://api.example.com/v1/data",
			binaries: []string{"curl"},
			domains:  []string{"api.example.com"},
		},
		{
			name:     "piped commands extract multiple binaries",
			cmd:      "ps -ax | grep postgres | awk '{print $1}'",
			binaries: []string{"awk", "grep", "ps"},
			domains:  []string{},
		},
		{
			name:      "builtins excluded",
			cmd:       "echo hello && cd /tmp && export FOO=bar",
			readPaths: []string{"/tmp"},
			binaries:  []string{},
			domains:   []string{},
		},
		{
			name:       "redirect creates write path",
			cmd:        "echo data > /home/user/project/out.txt",
			writePaths: []string{"out.txt"},
			binaries:   []string{},
			domains:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := AnalyzeToolUse(claude.ToolUse{
				Tool:  "Bash",
				Input: map[string]any{"command": tt.cmd},
			}, "/home/user/project")

			assert.Equal(t, tt.readPaths, a.ReadPaths, "readPaths")
			assert.Equal(t, tt.writePaths, a.WritePaths, "writePaths")
			assert.Equal(t, tt.binaries, a.Binaries, "binaries")
			assert.Equal(t, tt.domains, a.Domains, "domains")
		})
	}
}

func TestFormatPathsWithIcons(t *testing.T) {
	tests := []struct {
		name       string
		readPaths  []string
		writePaths []string
		expected   string
	}{
		{
			name:       "reads and writes",
			readPaths:  []string{"src/main.go"},
			writePaths: []string{"pkg/out.go"},
			expected:   "⬇ src/main.go ⬆ pkg/out.go",
		},
		{
			name:      "reads only",
			readPaths: []string{"a.go", "b.go"},
			expected:  "⬇ a.go ⬇ b.go",
		},
		{
			name:       "writes only",
			writePaths: []string{"c.go"},
			expected:   "⬆ c.go",
		},
		{
			name:     "empty",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, FormatPathsWithIcons(tt.readPaths, tt.writePaths))
		})
	}
}
