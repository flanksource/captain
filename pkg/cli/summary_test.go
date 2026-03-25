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
		{Tool: "Bash", Input: map[string]any{"command": "go build ./..."}, ProjectRoot: "/project", InputTokens: 10, OutputTokens: 50},
		{Tool: "Bash", Input: map[string]any{"command": "echo $HOME"}, ProjectRoot: "/project", InputTokens: 5, OutputTokens: 20},
		{Tool: "Read", Input: map[string]any{"file_path": "/project/pkg/cli/history.go"}, ProjectRoot: "/project", InputTokens: 8, OutputTokens: 200},
		{Tool: "Read", Input: map[string]any{"file_path": "/project/pkg/cli/types.go"}, ProjectRoot: "/project", InputTokens: 8, OutputTokens: 100},
		{Tool: "Write", Input: map[string]any{"file_path": "/project/pkg/cli/summary.go"}, ProjectRoot: "/project", InputTokens: 50, OutputTokens: 10},
		{Tool: "Edit", Input: map[string]any{"file_path": "/project/cmd/main.go"}, ProjectRoot: "/project", InputTokens: 30, OutputTokens: 5},
		{Tool: "WebFetch", Input: map[string]any{"url": "https://docs.example.com/api"}, ProjectRoot: "/project", InputTokens: 5, OutputTokens: 500},
	}

	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())
	result := BuildSummary(toolUses, classifier, nil)

	assert.Equal(t, 7, result.TotalToolUses)
	assert.Equal(t, 0, result.DeniedCount)

	toolMap := make(map[string]UsageSummary)
	for _, tc := range result.Tools {
		toolMap[tc.Name] = tc
	}
	assert.Equal(t, 2, toolMap["Bash"].Count)
	assert.Equal(t, 2, toolMap["Read"].Count)
	assert.Equal(t, 1, toolMap["Write"].Count)
	assert.Equal(t, 1, toolMap["Edit"].Count)
	assert.Equal(t, 1, toolMap["WebFetch"].Count)

	// WebFetch has most tokens (505), should be first
	assert.Equal(t, "WebFetch", result.Tools[0].Name)
	assert.NotEmpty(t, result.Tools[0].Tokens)

	assert.NotEmpty(t, result.Paths)

	domainMap := make(map[string]UsageSummary)
	for _, d := range result.Domains {
		domainMap[d.Name] = d
	}
	assert.Equal(t, 1, domainMap["docs.example.com"].Count)

	envMap := make(map[string]UsageSummary)
	for _, e := range result.EnvVars {
		envMap[e.Name] = e
	}
	assert.Equal(t, 1, envMap["HOME"].Count)
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

func TestToUsageSummaries_SortedByTokensDesc(t *testing.T) {
	counts := map[string]int{"Read": 10, "Bash": 20, "Write": 5}
	tokens := map[string]int{"Read": 500, "Bash": 100, "Write": 1000}

	result := toUsageSummaries(counts, tokens)

	// Sorted by tokens desc: Write(1000), Read(500), Bash(100)
	assert.Equal(t, "Write", result[0].Name)
	assert.Equal(t, 5, result[0].Count)
	assert.Equal(t, "Read", result[1].Name)
	assert.Equal(t, 10, result[1].Count)
	assert.Equal(t, "Bash", result[2].Name)
	assert.Equal(t, 20, result[2].Count)
}

func TestToUsageSummaries_NoTokensFallsBackToCount(t *testing.T) {
	counts := map[string]int{"Read": 10, "Bash": 20, "Write": 5}

	result := toUsageSummaries(counts, nil)

	// All tokens=0, so sorted by count desc: Bash(20), Read(10), Write(5)
	assert.Equal(t, "Bash", result[0].Name)
	assert.Equal(t, "Read", result[1].Name)
	assert.Equal(t, "Write", result[2].Name)
}

func TestBuildSummary_ToolErrors(t *testing.T) {
	toolUses := []claude.ToolUse{
		{Tool: "Bash", Input: map[string]any{"command": "ls"}, ProjectRoot: "/p", InputTokens: 5, OutputTokens: 10},
		{Tool: "Bash", Input: map[string]any{"command": "false"}, ProjectRoot: "/p", InputTokens: 5, OutputTokens: 10, IsError: true},
		{Tool: "Read", Input: map[string]any{"file_path": "/p/f.go"}, ProjectRoot: "/p", InputTokens: 5, OutputTokens: 100},
	}

	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())
	result := BuildSummary(toolUses, classifier, nil)

	toolMap := make(map[string]UsageSummary)
	for _, tc := range result.Tools {
		toolMap[tc.Name] = tc
	}
	assert.Equal(t, 1, toolMap["Bash"].Errors)
	assert.Equal(t, 0, toolMap["Read"].Errors)
}
