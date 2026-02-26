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
			name:     "wget with http",
			cmd:      `wget http://files.example.org/archive.tar.gz`,
			expected: []string{"files.example.org"},
		},
		{
			name:     "git clone with url",
			cmd:      `git clone https://github.com/user/repo.git`,
			expected: []string{"github.com"},
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
			got := sortedKeys(domains)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestExtractDomains(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		input    map[string]any
		contains []string
	}{
		{
			name:     "gh command",
			tool:     "Bash",
			input:    map[string]any{"command": "gh pr create --title test"},
			contains: []string{"github.com", "api.github.com"},
		},
		{
			name:     "npm install",
			tool:     "Bash",
			input:    map[string]any{"command": "npm install express"},
			contains: []string{"registry.npmjs.org"},
		},
		{
			name:     "go get",
			tool:     "Bash",
			input:    map[string]any{"command": "go get github.com/pkg/errors"},
			contains: []string{"proxy.golang.org", "sum.golang.org"},
		},
		{
			name:     "pip install",
			tool:     "Bash",
			input:    map[string]any{"command": "pip install requests"},
			contains: []string{"pypi.org"},
		},
		{
			name:     "curl with url",
			tool:     "Bash",
			input:    map[string]any{"command": "curl https://api.stripe.com/v1/charges"},
			contains: []string{"api.stripe.com"},
		},
		{
			name:     "non-bash tool ignored",
			tool:     "Read",
			input:    map[string]any{"file_path": "/tmp/test.go"},
			contains: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domains := make(map[string]bool)
			extractDomains(claude.ToolUse{Tool: tt.tool, Input: tt.input}, domains)
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
			name:        "relative to project",
			path:        "/home/user/project/pkg/cli/history.go",
			projectRoot: "/home/user/project",
			expected:    "pkg/cli/",
		},
		{
			name:        "deep path collapses to 2 levels",
			path:        "/home/user/project/pkg/cli/sub/deep/file.go",
			projectRoot: "/home/user/project",
			expected:    "pkg/cli/",
		},
		{
			name:        "root level file",
			path:        "/home/user/project/main.go",
			projectRoot: "/home/user/project",
			expected:    "", // dir is "." which is skipped
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

func TestExtractWritePathsFromBash(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "redirect",
			cmd:      `echo hello > /tmp/out.txt`,
			expected: []string{"/tmp/out.txt"},
		},
		{
			name:     "append redirect",
			cmd:      `echo hello >> /tmp/log.txt`,
			expected: []string{"/tmp/log.txt"},
		},
		{
			name:     "mkdir",
			cmd:      `mkdir -p /tmp/newdir`,
			expected: []string{"/tmp/newdir"},
		},
		{
			name:     "no writes",
			cmd:      `echo hello`,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWritePathsFromBash(tt.cmd)
			assert.Equal(t, tt.expected, got)
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
	}
	generated := SRTConfig{
		Network: SRTNetwork{
			AllowedDomains: []string{"github.com", "new.com"},
			DeniedDomains:  []string{},
		},
		Filesystem: SRTFilesystem{
			DenyRead:   []string{"~/.ssh", "~/.gnupg"},
			AllowWrite: []string{".", "/tmp", "pkg/cli/"},
			DenyWrite:  []string{".env"},
		},
	}

	merged := mergeSRTConfigs(existing, generated)

	assert.Equal(t, []string{"existing.com", "github.com", "new.com"}, merged.Network.AllowedDomains)
	assert.Equal(t, []string{"bad.com"}, merged.Network.DeniedDomains)
	assert.Equal(t, []string{"~/.gnupg", "~/.ssh"}, merged.Filesystem.DenyRead)
	assert.Equal(t, []string{".", "/tmp", "old-dir/", "pkg/cli/"}, merged.Filesystem.AllowWrite)
	assert.Equal(t, []string{".env", "secrets/"}, merged.Filesystem.DenyWrite)
}

func TestLoadAndMergeSRTConfig(t *testing.T) {
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
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	require.NoError(t, os.WriteFile(existingPath, data, 0644))

	loaded, err := loadSRTConfig(existingPath)
	require.NoError(t, err)
	assert.Equal(t, existing, loaded)
}
