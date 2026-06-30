package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExitPlanTool_SurfacesPlanFilePath(t *testing.T) {
	tool := NewTool(BaseTool{
		RawTool:     "ExitPlanMode",
		ProjectRoot: "/home/u",
		Input: map[string]any{
			"planFilePath": "/home/u/.claude/plans/tidy-otter.md",
			"plan":         "# Tidy otter",
		},
	})

	exit, ok := tool.(*ExitPlanTool)
	if !ok {
		t.Fatalf("expected *ExitPlanTool, got %T", tool)
	}
	assert.Equal(t, "/home/u/.claude/plans/tidy-otter.md", exit.FilePath())
	assert.Equal(t, ".claude/plans/tidy-otter.md", exit.ExtractPath())

	pretty := exit.Pretty().String()
	assert.Contains(t, pretty, "tidy-otter")
	assert.Contains(t, pretty, "approved")
}

func TestExitPlanTool_NoPlanFilePath(t *testing.T) {
	exit := &ExitPlanTool{}
	assert.Equal(t, "", exit.FilePath())
	assert.Equal(t, "", exit.ExtractPath())
}
