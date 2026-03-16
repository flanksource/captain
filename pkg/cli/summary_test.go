package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
)

func TestExtractEnvVars(t *testing.T) {
	tests := []struct {
		name     string
		tu       claude.ToolUse
		expected []string
	}{
		{
			name: "bash with $VAR",
			tu: claude.ToolUse{
				Tool:  "Bash",
				Input: map[string]any{"command": "echo $HOME && cd $GOPATH/src"},
			},
			expected: []string{"GOPATH", "HOME"},
		},
		{
			name: "bash with ${VAR}",
			tu: claude.ToolUse{
				Tool:  "Bash",
				Input: map[string]any{"command": "echo ${MY_VAR} ${OTHER_VAR}"},
			},
			expected: []string{"MY_VAR", "OTHER_VAR"},
		},
		{
			name: "bash export references value var",
			tu: claude.ToolUse{
				Tool:  "Bash",
				Input: map[string]any{"command": "export FOO=$BAR"},
			},
			expected: []string{"BAR"},
		},
		{
			name: "webfetch with env in URL",
			tu: claude.ToolUse{
				Tool:  "WebFetch",
				Input: map[string]any{"url": "https://api.example.com/$API_VERSION/data"},
			},
			expected: []string{"API_VERSION"},
		},
		{
			name: "mcp tool with env vars",
			tu: claude.ToolUse{
				Tool:  "mcp__custom__run",
				Input: map[string]any{"config": "token=$AUTH_TOKEN", "path": "/tmp"},
			},
			expected: []string{"AUTH_TOKEN"},
		},
		{
			name: "read tool ignored",
			tu: claude.ToolUse{
				Tool:  "Read",
				Input: map[string]any{"file_path": "/home/$USER/file"},
			},
			expected: []string{},
		},
		{
			name: "no env vars",
			tu: claude.ToolUse{
				Tool:  "Bash",
				Input: map[string]any{"command": "echo hello"},
			},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExtractEnvVars(tt.tu))
		})
	}
}

func TestBuildSummary(t *testing.T) {
	toolUses := []claude.ToolUse{
		{Tool: "Bash", Input: map[string]any{"command": "go build ./..."}, ProjectRoot: "/project"},
		{Tool: "Bash", Input: map[string]any{"command": "echo $HOME"}, ProjectRoot: "/project"},
		{Tool: "Read", Input: map[string]any{"file_path": "/project/pkg/cli/history.go"}, ProjectRoot: "/project"},
		{Tool: "Read", Input: map[string]any{"file_path": "/project/pkg/cli/types.go"}, ProjectRoot: "/project"},
		{Tool: "Write", Input: map[string]any{"file_path": "/project/pkg/cli/summary.go"}, ProjectRoot: "/project"},
		{Tool: "Edit", Input: map[string]any{"file_path": "/project/cmd/main.go"}, ProjectRoot: "/project"},
		{Tool: "WebFetch", Input: map[string]any{"url": "https://docs.example.com/api"}, ProjectRoot: "/project"},
	}

	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())
	result := BuildSummary(toolUses, classifier, nil)

	assert.Equal(t, 7, result.TotalToolUses)
	assert.Equal(t, 0, result.DeniedCount)

	toolMap := make(map[string]int)
	for _, tc := range result.Tools {
		toolMap[tc.Name] = tc.Count
	}
	assert.Equal(t, 2, toolMap["Bash"])
	assert.Equal(t, 2, toolMap["Read"])
	assert.Equal(t, 1, toolMap["Write"])
	assert.Equal(t, 1, toolMap["Edit"])
	assert.Equal(t, 1, toolMap["WebFetch"])

	assert.NotEmpty(t, result.Paths)

	domainMap := make(map[string]int)
	for _, d := range result.Domains {
		domainMap[d.Name] = d.Count
	}
	assert.Equal(t, 1, domainMap["docs.example.com"])

	envMap := make(map[string]int)
	for _, e := range result.EnvVars {
		envMap[e.Name] = e.Count
	}
	assert.Equal(t, 1, envMap["HOME"])
}

func TestBuildSummary_DeniedCounts(t *testing.T) {
	toolUses := []claude.ToolUse{
		{Tool: "Bash", Input: map[string]any{"command": "ls"}, ProjectRoot: "/p"},
		{Tool: "Bash", Input: map[string]any{"command": "rm -rf /"}, Denied: true, DeniedReason: "dangerous", ProjectRoot: "/p"},
	}

	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())
	result := BuildSummary(toolUses, classifier, nil)

	assert.Equal(t, 2, result.TotalToolUses)
	assert.Equal(t, 1, result.DeniedCount)
}

func TestBuildSummary_WithCosts(t *testing.T) {
	costs := []claude.SessionCost{
		{
			Tokens: claude.TokenSummary{
				InputTokens:      1000,
				OutputTokens:     500,
				CacheReadTokens:  200,
				CacheWriteTokens: 100,
				TotalCost:        0.05,
			},
		},
		{
			Tokens: claude.TokenSummary{
				InputTokens:      2000,
				OutputTokens:     1000,
				CacheReadTokens:  800,
				CacheWriteTokens: 200,
				TotalCost:        0.10,
			},
		},
	}

	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())
	result := BuildSummary(nil, classifier, costs)

	assert.Equal(t, 3000, result.Cost.InputTokens)
	assert.Equal(t, 1500, result.Cost.OutputTokens)
	assert.Equal(t, 1000, result.Cost.CacheReadTokens)
	assert.Equal(t, 300, result.Cost.CacheWriteTokens)
	assert.Equal(t, "$0.1500", result.Cost.CostDisplay)
	// CacheHitRatio: 1000 / (3000 + 1000 + 300) = 23.26%
	assert.InDelta(t, 23.26, result.Cost.CacheHitRatio, 0.01)
}

func TestToNameCounts_SortedByCountDesc(t *testing.T) {
	m := map[string]int{"Read": 10, "Bash": 20, "Write": 5}
	result := toNameCounts(m)

	assert.Equal(t, "Bash", result[0].Name)
	assert.Equal(t, 20, result[0].Count)
	assert.Equal(t, "Read", result[1].Name)
	assert.Equal(t, 10, result[1].Count)
	assert.Equal(t, "Write", result[2].Name)
	assert.Equal(t, 5, result[2].Count)
}

func TestToPathSummaries(t *testing.T) {
	reads := map[string]int{"pkg/cli/history.go": 5, "cmd/main.go": 1}
	writes := map[string]int{"pkg/cli/history.go": 3, "pkg/claude/cost.go": 2}

	result := toPathSummaries(reads, writes)

	assert.Len(t, result, 3)
	// sorted alphabetically
	assert.Equal(t, "cmd/main.go", result[0].Path)
	assert.Equal(t, 1, result[0].ReadCount)
	assert.Equal(t, 0, result[0].WriteCount)
	assert.Equal(t, "pkg/claude/cost.go", result[1].Path)
	assert.Equal(t, "pkg/cli/history.go", result[2].Path)
	assert.Equal(t, 5, result[2].ReadCount)
	assert.Equal(t, 3, result[2].WriteCount)
}
