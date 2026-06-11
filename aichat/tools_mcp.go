package aichat

import (
	"context"
	"fmt"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/mcp"
)

// MCPServer configures one external MCP server the chat backend consumes as a
// tool source. Exactly one transport must be set.
type MCPServer struct {
	Name           string
	Command        string            // stdio: executable
	Args           []string          // stdio: args
	Env            []string          // stdio: env
	URL            string            // remote: base URL (SSE / streamable HTTP)
	Headers        map[string]string // remote: headers
	StreamableHTTP bool              // remote: use streamable HTTP instead of SSE
}

// MCPTools connects to the configured MCP servers via Genkit's MCP host and
// returns their active tools as ToolRefs. Returns nil when no servers configured.
func MCPTools(ctx context.Context, g *genkit.Genkit, servers []MCPServer) ([]ai.ToolRef, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	cfgs := make([]mcp.MCPServerConfig, 0, len(servers))
	for _, s := range servers {
		opts, err := clientOptions(s)
		if err != nil {
			return nil, err
		}
		cfgs = append(cfgs, mcp.MCPServerConfig{Name: s.Name, Config: opts})
	}
	host, err := mcp.NewMCPHost(g, mcp.MCPHostOptions{Name: "captain-aichat", MCPServers: cfgs})
	if err != nil {
		return nil, fmt.Errorf("mcp host: %w", err)
	}
	tools, err := host.GetActiveTools(ctx, g)
	if err != nil {
		return nil, fmt.Errorf("mcp tools: %w", err)
	}
	refs := make([]ai.ToolRef, len(tools))
	for i, t := range tools {
		refs[i] = t
	}
	return refs, nil
}

func clientOptions(s MCPServer) (mcp.MCPClientOptions, error) {
	opts := mcp.MCPClientOptions{Name: s.Name}
	switch {
	case s.Command != "":
		opts.Stdio = &mcp.StdioConfig{Command: s.Command, Args: s.Args, Env: s.Env}
	case s.URL != "" && s.StreamableHTTP:
		opts.StreamableHTTP = &mcp.StreamableHTTPConfig{BaseURL: s.URL, Headers: s.Headers}
	case s.URL != "":
		opts.SSE = &mcp.SSEConfig{BaseURL: s.URL, Headers: s.Headers}
	default:
		return opts, fmt.Errorf("mcp server %q has no transport (set Command or URL)", s.Name)
	}
	return opts, nil
}
