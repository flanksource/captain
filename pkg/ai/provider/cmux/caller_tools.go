package cmux

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
)

// emptyClaudeMCPConfig is the --mcp-config document with no servers.
const emptyClaudeMCPConfig = `{"mcpServers":{}}`

// callerToolLaunch is what the pasted agent command needs to reach the run's
// caller-tool server. The credential never appears in it: claude reads it
// from a 0600 --mcp-config file, codex from environment variables the shell
// fills from 0600 files.
type callerToolLaunch struct {
	allowedTools []string
	// configOverrides are codex `-c` values.
	configOverrides []string
	// envPrefix assigns the codex credential variables ahead of the command.
	envPrefix string
}

// prepareCallerTools wires callerTools (nil when the run has none) and
// mcp.disabled into the agent's launch, editing the claude extra args in place.
func prepareCallerTools(extra any, req ai.Request, callerTools *callertools.CLIRun) (callerToolLaunch, error) {
	switch opts := extra.(type) {
	case *api.ClaudeCmuxOptions:
		return claudeCallerTools(opts, req.Permissions.MCP.Disabled, callerTools)
	case *api.CodexCmuxOptions:
		return codexCallerTools(req.Permissions.MCP.Disabled, callerTools)
	default:
		return callerToolLaunch{}, fmt.Errorf("cmux caller tools: unexpected extra args %T", extra)
	}
}

func claudeCallerTools(opts *api.ClaudeCmuxOptions, mcpDisabled bool, callerTools *callertools.CLIRun) (callerToolLaunch, error) {
	if mcpDisabled && len(opts.MCPConfig) > 0 {
		return callerToolLaunch{}, fmt.Errorf("cmux: cliArgs.mcpConfig conflicts with disabled MCP")
	}
	opts.StrictMCPConfig = opts.StrictMCPConfig || mcpDisabled
	if callerTools == nil {
		if mcpDisabled {
			opts.MCPConfig = []string{emptyClaudeMCPConfig}
		}
		return callerToolLaunch{}, nil
	}
	path, err := callerTools.ClaudeMCPConfigFile()
	if err != nil {
		return callerToolLaunch{}, fmt.Errorf("cmux: %w", err)
	}
	opts.MCPConfig = append(slices.Clone(opts.MCPConfig), path)
	return callerToolLaunch{allowedTools: callerTools.ClaudeAllowedTools()}, nil
}

func codexCallerTools(mcpDisabled bool, callerTools *callertools.CLIRun) (callerToolLaunch, error) {
	if callerTools == nil {
		if mcpDisabled {
			return callerToolLaunch{configOverrides: []string{"mcp_servers={}"}}, nil
		}
		return callerToolLaunch{}, nil
	}
	override, err := callerTools.CodexConfig(mcpDisabled)
	if err != nil {
		return callerToolLaunch{}, fmt.Errorf("cmux: %w", err)
	}
	files, err := callerTools.CodexEnvFiles()
	if err != nil {
		return callerToolLaunch{}, fmt.Errorf("cmux: %w", err)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	assigns := make([]string, 0, len(names))
	for _, name := range names {
		assigns = append(assigns, name+`="$(cat `+shellSingleQuote(files[name])+`)"`)
	}
	return callerToolLaunch{configOverrides: []string{override}, envPrefix: strings.Join(assigns, " ")}, nil
}

// launchCommand prepends the credential assignments to the agent command.
func (l callerToolLaunch) launchCommand(command string) string {
	if l.envPrefix == "" {
		return command
	}
	return l.envPrefix + " " + command
}
