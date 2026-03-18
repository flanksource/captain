package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractURLDomains(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "curl with https",
			cmd:      `curl https://api.example.com/v1/data`,
			expected: []string{"api.example.com"},
		},
		{
			name:     "multiple urls",
			cmd:      `curl https://a.com/x && wget https://b.com/y`,
			expected: []string{"a.com", "b.com"},
		},
		{
			name:     "no urls",
			cmd:      `echo hello`,
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domains := make(map[string]bool)
			extractURLDomains(tt.cmd, domains)
			assert.Equal(t, tt.expected, sortedKeys(domains))
		})
	}
}

func TestExtractDomains(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		contains []string
	}{
		{
			name:     "gh command",
			input:    map[string]any{"command": "gh pr create --title test"},
			contains: []string{"github.com", "api.github.com"},
		},
		{
			name:     "npm install",
			input:    map[string]any{"command": "npm install express"},
			contains: []string{"registry.npmjs.org"},
		},
		{
			name:     "go get",
			input:    map[string]any{"command": "go get github.com/pkg/errors"},
			contains: []string{"proxy.golang.org", "sum.golang.org"},
		},
		{
			name:     "curl with url",
			input:    map[string]any{"command": "curl https://api.stripe.com/v1/charges"},
			contains: []string{"api.stripe.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domains := make(map[string]bool)
			extractDomains(claude.ToolUse{Tool: "Bash", Input: tt.input}, domains)
			for _, d := range tt.contains {
				assert.True(t, domains[d], "expected domain %s", d)
			}
		})
	}
}

func TestAddDir(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		projectRoot string
		expected    string
	}{
		{
			name:        "relative to project - top level only",
			path:        "/home/user/project/pkg/cli/history.go",
			projectRoot: "/home/user/project",
			expected:    "pkg/",
		},
		{
			name:        "deep path collapses to top level",
			path:        "/home/user/project/pkg/cli/sub/deep/file.go",
			projectRoot: "/home/user/project",
			expected:    "pkg/",
		},
		{
			name:        "root level file skipped",
			path:        "/home/user/project/main.go",
			projectRoot: "/home/user/project",
			expected:    "",
		},
		{
			name:        "single dir kept",
			path:        "/home/user/project/cmd/main.go",
			projectRoot: "/home/user/project",
			expected:    "cmd/",
		},
		{
			name:        "absolute path outside project skipped",
			path:        "/usr/local/bin/something",
			projectRoot: "/home/user/project",
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirs := make(map[string]bool)
			addDir(dirs, tt.path, tt.projectRoot)
			if tt.expected == "" {
				assert.Empty(t, dirs)
			} else {
				assert.True(t, dirs[tt.expected], "expected dir %s in %v", tt.expected, dirs)
			}
		})
	}
}

func TestCollapseToTopDirs(t *testing.T) {
	tests := []struct {
		name     string
		dirs     map[string]bool
		expected []string
	}{
		{
			name: "child dirs removed",
			dirs: map[string]bool{
				".": true, "/tmp": true,
				"pkg/":     true,
				"pkg/cli/": true,
				"cmd/":     true,
			},
			expected: []string{".", "/tmp", "cmd/", "pkg/"},
		},
		{
			name: "no overlap",
			dirs: map[string]bool{
				".": true, "src/": true, "test/": true,
			},
			expected: []string{".", "src/", "test/"},
		},
		{
			name:     "empty",
			dirs:     map[string]bool{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collapseToTopDirs(tt.dirs)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestExtractBinaries(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "simple command",
			cmd:      "go build ./...",
			expected: []string{"go"},
		},
		{
			name:     "piped commands",
			cmd:      "ps -ax | grep postgres | awk '{print $1}'",
			expected: []string{"awk", "grep", "ps"},
		},
		{
			name:     "chained commands",
			cmd:      "git add . && git commit -m test",
			expected: []string{"git"},
		},
		{
			name:     "skips builtins",
			cmd:      "echo hello && cd /tmp && export FOO=bar",
			expected: []string{},
		},
		{
			name:     "absolute path binary",
			cmd:      "/usr/local/bin/golangci-lint run",
			expected: []string{"golangci-lint"},
		},
		{
			name:     "multiple distinct",
			cmd:      "make build && go test ./... && golangci-lint run",
			expected: []string{"go", "golangci-lint", "make"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binaries := make(map[string]bool)
			extractBinaries(claude.ToolUse{
				Tool:  "Bash",
				Input: map[string]any{"command": tt.cmd},
			}, binaries)
			assert.Equal(t, tt.expected, sortedKeys(binaries))
		})
	}
}

func TestMergeSRTConfigs(t *testing.T) {
	existing := SRTConfig{
		Network: SRTNetwork{
			AllowedDomains: []string{"github.com", "existing.com"},
			DeniedDomains:  []string{"bad.com"},
		},
		Filesystem: SRTFilesystem{
			DenyRead:   []string{"~/.ssh"},
			AllowWrite: []string{".", "old-dir/"},
			DenyWrite:  []string{".env", "secrets/"},
		},
		Environment: SRTEnvironment{
			Passthrough: []string{"HOME", "PATH", "CUSTOM_VAR"},
		},
		Binaries: []string{"git", "make"},
	}
	generated := SRTConfig{
		Network: SRTNetwork{
			AllowedDomains: []string{"github.com", "new.com"},
			DeniedDomains:  []string{},
		},
		Filesystem: SRTFilesystem{
			DenyRead:   []string{"~/.ssh", "~/.gnupg"},
			AllowWrite: []string{".", "/tmp", "pkg/"},
			DenyWrite:  []string{".env"},
		},
		Environment: SRTEnvironment{
			Passthrough: []string{"HOME", "PATH", "GOPATH"},
		},
		Binaries: []string{"go", "make"},
	}

	merged := mergeSRTConfigs(existing, generated)

	assert.Equal(t, []string{"existing.com", "github.com", "new.com"}, merged.Network.AllowedDomains)
	assert.Equal(t, []string{"bad.com"}, merged.Network.DeniedDomains)
	assert.Equal(t, []string{"~/.gnupg", "~/.ssh"}, merged.Filesystem.DenyRead)
	assert.Equal(t, []string{".", "/tmp", "old-dir/", "pkg/"}, merged.Filesystem.AllowWrite)
	assert.Equal(t, []string{".env", "secrets/"}, merged.Filesystem.DenyWrite)
	assert.Equal(t, []string{"CUSTOM_VAR", "GOPATH", "HOME", "PATH"}, merged.Environment.Passthrough)
	assert.Equal(t, []string{"git", "go", "make"}, merged.Binaries)
}

func TestLoadSRTConfig(t *testing.T) {
	tmpDir := t.TempDir()
	existingPath := filepath.Join(tmpDir, "srt-settings.json")

	existing := SRTConfig{
		Network: SRTNetwork{
			AllowedDomains: []string{"github.com"},
			DeniedDomains:  []string{},
		},
		Filesystem: SRTFilesystem{
			DenyRead:   []string{"~/.ssh"},
			AllowWrite: []string{"."},
			DenyWrite:  []string{".env"},
		},
		Environment: SRTEnvironment{
			Passthrough: []string{"HOME", "PATH"},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	require.NoError(t, os.WriteFile(existingPath, data, 0644))

	loaded, err := loadSRTConfig(existingPath)
	require.NoError(t, err)
	assert.Equal(t, existing, loaded)
}

func TestClassifyEnvVar(t *testing.T) {
	passthrough := []string{"HOME", "PATH", "LC_*", "GOPATH"}

	tests := []struct {
		name     string
		envVar   string
		expected string
	}{
		{name: "exact match passthrough", envVar: "HOME", expected: "passthrough"},
		{name: "glob prefix passthrough", envVar: "LC_ALL", expected: "passthrough"},
		{name: "blocked suffix pattern", envVar: "GITHUB_TOKEN", expected: "blocked"},
		{name: "blocked prefix pattern", envVar: "AWS_ACCESS_KEY_ID", expected: "blocked"},
		{name: "blocked exact", envVar: "KUBECONFIG", expected: "blocked"},
		{name: "blocked SSH", envVar: "SSH_AUTH_SOCK", expected: "blocked"},
		{name: "ignored unknown", envVar: "MY_CUSTOM_THING", expected: "ignored"},
		{name: "passthrough beats blocked", envVar: "GOPATH", expected: "passthrough"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, classifyEnvVar(tt.envVar, passthrough))
		})
	}
}

func TestMatchEnvPattern(t *testing.T) {
	tests := []struct {
		pattern  string
		name     string
		expected bool
	}{
		{"HOME", "HOME", true},
		{"HOME", "HOMEDIR", false},
		{"LC_*", "LC_ALL", true},
		{"LC_*", "LC_", true},
		{"LC_*", "XLC_ALL", false},
		{"*_TOKEN", "GITHUB_TOKEN", true},
		{"*_TOKEN", "TOKEN", false},
		{"*_TOKEN", "TOKENIZER", false},
		{"AWS_*", "AWS_SECRET_KEY", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, matchEnvPattern(tt.pattern, tt.name))
		})
	}
}
