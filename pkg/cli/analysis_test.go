package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
)

func TestAnalyzeToolUse_Read(t *testing.T) {
	a := AnalyzeToolUseLegacy(claude.ToolUse{
		Tool:  "Read",
		Input: map[string]any{"file_path": "/home/user/project/src/main.go"},
	}, "/home/user/project")

	assert.Equal(t, []string{"/home/user/project/src/main.go"}, a.ReadPaths)
	assert.Empty(t, a.WritePaths)
	assert.Empty(t, a.Binaries)
	assert.Empty(t, a.Domains)
}

// TestAnalyzeToolUse_AnchorsRelativePathsToCWD is the case that made session
// paths unusable: a bash command's relative path means nothing without the
// directory that command ran in, and an agent's cwd moves during a session.
func TestAnalyzeToolUse_AnchorsRelativePathsToCWD(t *testing.T) {
	a := AnalyzeToolUseLegacy(claude.ToolUse{
		Tool:  "Bash",
		Input: map[string]any{"command": "touch out.txt"},
		CWD:   "/home/user/project/pkg/cli",
	}, "/home/user/project")

	assert.Equal(t, []string{"/home/user/project/pkg/cli/out.txt"}, a.WritePaths)
}

// A tool use with no cwd recorded falls back to the project root rather than
// leaving the path dangling.
func TestAnalyzeToolUse_FallsBackToProjectRootWithoutCWD(t *testing.T) {
	a := AnalyzeToolUseLegacy(claude.ToolUse{
		Tool:  "Bash",
		Input: map[string]any{"command": "touch out.txt"},
	}, "/home/user/project")

	assert.Equal(t, []string{"/home/user/project/out.txt"}, a.WritePaths)
}

func TestAnalyzeToolUse_Grep(t *testing.T) {
	a := AnalyzeToolUseLegacy(claude.ToolUse{
		Tool:  "Grep",
		Input: map[string]any{"pattern": "TODO", "path": "/home/user/project/pkg/"},
	}, "/home/user/project")

	assert.Equal(t, []string{"/home/user/project/pkg/"}, a.ReadPaths)
	assert.Empty(t, a.WritePaths)
}

func TestAnalyzeToolUse_Glob(t *testing.T) {
	a := AnalyzeToolUseLegacy(claude.ToolUse{
		Tool:  "Glob",
		Input: map[string]any{"pattern": "**/*.go", "path": "/home/user/project/pkg"},
	}, "/home/user/project")

	assert.Equal(t, []string{"/home/user/project/pkg"}, a.ReadPaths)
	assert.Empty(t, a.WritePaths)
}

// TestAnalyzeToolUse_FileWritingTools covers every tool that writes a file.
// MultiEdit and NotebookEdit used to be dropped, which made the write set least
// accurate exactly where it is used — to stage a commit. NotebookEdit is also
// why the path cannot just be read off file_path: it names its file
// notebook_path.
func TestAnalyzeToolUse_FileWritingTools(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		cwd   string
		want  string
	}{
		{
			tool:  "Write",
			input: map[string]any{"file_path": "/home/user/project/pkg/cli/new.go", "content": "package cli"},
			want:  "/home/user/project/pkg/cli/new.go",
		},
		{
			tool:  "Edit",
			input: map[string]any{"file_path": "/home/user/project/cmd/main.go"},
			want:  "/home/user/project/cmd/main.go",
		},
		{
			tool:  "MultiEdit",
			input: map[string]any{"file_path": "/home/user/project/pkg/api/spec.go"},
			want:  "/home/user/project/pkg/api/spec.go",
		},
		{
			tool:  "NotebookEdit",
			input: map[string]any{"notebook_path": "/home/user/project/analysis.ipynb"},
			want:  "/home/user/project/analysis.ipynb",
		},
		{
			// The write set is absolute data shared across processes, so a
			// relative path is still anchored to the tool use's own cwd.
			tool:  "MultiEdit",
			input: map[string]any{"file_path": "spec.go"},
			cwd:   "/home/user/project/pkg/api",
			want:  "/home/user/project/pkg/api/spec.go",
		},
	}

	for _, tc := range cases {
		t.Run(tc.tool+" "+tc.want, func(t *testing.T) {
			a := AnalyzeToolUseLegacy(claude.ToolUse{
				Tool:  tc.tool,
				Input: tc.input,
				CWD:   tc.cwd,
			}, "/home/user/project")

			assert.Equal(t, []string{tc.want}, a.WritePaths)
			assert.Empty(t, a.ReadPaths)
		})
	}
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
			readPaths:  []string{"/home/user/project/output.txt"},
			writePaths: []string{"/home/user/project/output.txt"},
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
			writePaths: []string{"/home/user/project/out.txt"},
			binaries:   []string{},
			domains:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := AnalyzeToolUseLegacy(claude.ToolUse{
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

func TestAnalyzeToolUse_WebFetch(t *testing.T) {
	a := AnalyzeToolUseLegacy(claude.ToolUse{
		Tool:  "WebFetch",
		Input: map[string]any{"url": "https://docs.example.com/api/v1"},
	}, "")

	assert.Equal(t, []string{"docs.example.com"}, a.Domains)
	assert.Empty(t, a.ReadPaths)
	assert.Empty(t, a.WritePaths)
}

func TestAnalyzeToolUse_WebSearch(t *testing.T) {
	a := AnalyzeToolUseLegacy(claude.ToolUse{
		Tool:  "WebSearch",
		Input: map[string]any{"query": "golang context"},
	}, "")

	assert.Equal(t, []string{"api.anthropic.com"}, a.Domains)
}

func TestAnalyzeToolUse_MCPWithURL(t *testing.T) {
	a := AnalyzeToolUseLegacy(claude.ToolUse{
		Tool:  "mcp__playwright__browser_navigate",
		Input: map[string]any{"url": "https://app.example.com/dashboard"},
	}, "")

	assert.Equal(t, []string{"app.example.com"}, a.Domains)
}

func TestAnalyzeToolUse_MCPWithoutURL(t *testing.T) {
	a := AnalyzeToolUseLegacy(claude.ToolUse{
		Tool:  "mcp__playwright__browser_click",
		Input: map[string]any{"selector": "#button"},
	}, "")

	assert.Empty(t, a.Domains)
}

func TestFormatPathsWithIcons(t *testing.T) {
	tests := []struct {
		name       string
		readPaths  []string
		writePaths []string
		expected   string
	}{
		{
			name:       "reads and writes show directories",
			readPaths:  []string{"src/main.go"},
			writePaths: []string{"pkg/out.go"},
			expected:   "⬇ src/ ⬆ pkg/",
		},
		{
			name:      "root-level files deduplicated",
			readPaths: []string{"a.go", "b.go"},
			expected:  "⬇ ./",
		},
		{
			name:       "writes only",
			writePaths: []string{"c.go"},
			expected:   "⬆ ./",
		},
		{
			name:     "empty",
			expected: "",
		},
		{
			name:      "directory paths preserved",
			readPaths: []string{"pkg/cli/"},
			expected:  "⬇ pkg/cli/",
		},
		{
			name:      "multiple files same dir deduplicated",
			readPaths: []string{"pkg/cli/a.go", "pkg/cli/b.go", "pkg/api/c.go"},
			expected:  "⬇ pkg/cli/ ⬇ pkg/api/",
		},
		{
			name:      "extensionless paths kept as-is",
			readPaths: []string{"Makefile", "Dockerfile"},
			expected:  "⬇ Makefile ⬇ Dockerfile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, FormatPathsWithIcons(tt.readPaths, tt.writePaths))
		})
	}
}
