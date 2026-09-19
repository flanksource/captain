package main

import (
	"testing"

	"github.com/flanksource/clicky/mcp"
	"github.com/stretchr/testify/require"
)

func TestMCPConfigExcludesDiagramsAnalyze(t *testing.T) {
	root := newRootCommand()
	config := newMCPConfig()
	registry := mcp.NewToolRegistry(config)

	analyze, _, err := root.Find([]string{"diagrams", "analyze"})
	require.NoError(t, err)
	require.Contains(t, config.Tools.Exclude, "^diagrams")
	require.NoError(t, registry.RegisterCommand(analyze))
	_, exposed := registry.GetTool("diagrams analyze")
	require.False(t, exposed, "local-only diagrams analyze must not be published as an MCP tool")
}
