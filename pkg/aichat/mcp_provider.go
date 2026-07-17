package aichat

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	genkitai "github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/mcp"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

// MCPServer configures one external MCP server. Exactly one transport must be
// set: Command for stdio, or URL for SSE/streamable HTTP.
type MCPServer struct {
	Name           string
	Command        string
	Args           []string
	Env            []string
	URL            string
	Headers        map[string]string
	StreamableHTTP bool
}

type MCPToolProviderOptions struct {
	Servers []MCPServer
}

type mcpClient interface {
	GetActiveTools(context.Context, *genkit.Genkit) ([]genkitai.Tool, error)
	Disconnect() error
}

type mcpClientFactory func(mcp.MCPClientOptions) (mcpClient, error)

// MCPToolProvider owns explicit Genkit MCP clients and their projected Captain
// tool definitions. Call Close when the mounted chat service shuts down.
type MCPToolProvider struct {
	options       MCPToolProviderOptions
	clientFactory mcpClientFactory

	mu      sync.Mutex
	clients map[string]mcpClient
	tools   *ToolSet
}

func NewMCPToolProvider(options MCPToolProviderOptions) *MCPToolProvider {
	options.Servers = append([]MCPServer(nil), options.Servers...)
	return &MCPToolProvider{
		options: options,
		clientFactory: func(options mcp.MCPClientOptions) (mcpClient, error) {
			return mcp.NewGenkitMCPClient(options)
		},
		clients: map[string]mcpClient{},
	}
}

func (p *MCPToolProvider) ToolSet(ctx context.Context) (ToolSet, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tools != nil {
		return cloneToolSet(*p.tools), nil
	}
	if err := p.validate(); err != nil {
		return ToolSet{}, err
	}

	set, err := p.load(ctx)
	if err != nil {
		return ToolSet{}, errors.Join(err, p.closeClientsLocked())
	}
	p.tools = &set
	return cloneToolSet(set), nil
}

func (p *MCPToolProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tools = nil
	return p.closeClientsLocked()
}

func (p *MCPToolProvider) validate() error {
	seen := make(map[string]bool, len(p.options.Servers))
	for _, server := range p.options.Servers {
		if server.Name == "" {
			return fmt.Errorf("MCP server name is required")
		}
		if seen[server.Name] {
			return fmt.Errorf("duplicate MCP server %q", server.Name)
		}
		seen[server.Name] = true
		if (server.Command == "") == (server.URL == "") {
			return fmt.Errorf("MCP server %q must configure exactly one of Command or URL", server.Name)
		}
	}
	return nil
}

func (p *MCPToolProvider) load(ctx context.Context) (ToolSet, error) {
	set := ToolSet{}
	seen := make(map[string]string)
	for _, server := range p.options.Servers {
		client, err := p.client(server)
		if err != nil {
			return ToolSet{}, fmt.Errorf("connect MCP server %q: %w", server.Name, err)
		}
		tools, err := client.GetActiveTools(ctx, nil)
		if err != nil {
			return ToolSet{}, fmt.Errorf("discover MCP tools from server %q: %w", server.Name, err)
		}
		for _, tool := range tools {
			if prior, exists := seen[tool.Name()]; exists {
				return ToolSet{}, fmt.Errorf("duplicate MCP tool definition %q from servers %q and %q", tool.Name(), prior, server.Name)
			}
			seen[tool.Name()] = server.Name
			definition, catalog, err := projectMCPTool(server.Name, tool)
			if err != nil {
				return ToolSet{}, err
			}
			set.Definitions = append(set.Definitions, definition)
			set.Catalog = append(set.Catalog, catalog)
		}
	}
	return set, nil
}

func (p *MCPToolProvider) client(server MCPServer) (mcpClient, error) {
	if client := p.clients[server.Name]; client != nil {
		return client, nil
	}
	options := mcp.MCPClientOptions{Name: server.Name}
	if server.Command != "" {
		options.Stdio = &mcp.StdioConfig{
			Command: server.Command,
			Args:    append([]string(nil), server.Args...),
			Env:     append([]string(nil), server.Env...),
		}
	} else if server.StreamableHTTP {
		options.StreamableHTTP = &mcp.StreamableHTTPConfig{BaseURL: server.URL, Headers: maps.Clone(server.Headers)}
	} else {
		options.SSE = &mcp.SSEConfig{BaseURL: server.URL, Headers: maps.Clone(server.Headers)}
	}
	client, err := p.clientFactory(options)
	if err != nil {
		return nil, err
	}
	p.clients[server.Name] = client
	return client, nil
}

func (p *MCPToolProvider) closeClientsLocked() error {
	var errs []error
	for _, server := range p.options.Servers {
		client := p.clients[server.Name]
		if client == nil {
			continue
		}
		if err := client.Disconnect(); err != nil {
			errs = append(errs, fmt.Errorf("disconnect MCP server %q: %w", server.Name, err))
		}
		delete(p.clients, server.Name)
	}
	return errors.Join(errs...)
}

func projectMCPTool(server string, tool genkitai.Tool) (api.ToolDefinition, aitools.ToolCatalogEntry, error) {
	definition := tool.Definition()
	if definition == nil {
		return api.ToolDefinition{}, aitools.ToolCatalogEntry{}, fmt.Errorf("MCP tool %q from server %q has no definition", tool.Name(), server)
	}
	inputSchema := maps.Clone(definition.InputSchema)
	catalog := aitools.ToolCatalogEntry{
		Name: definition.Name, Title: definition.Name, Description: definition.Description,
		Source: "mcp", Server: server, PreferenceKey: definition.Name,
		DefaultPermission: api.ToolModeAuto, InputSchema: aitools.ObjectSchema(inputSchema),
		OutputSchema: maps.Clone(definition.OutputSchema),
	}
	aitools.ApplyToolMetadata(&catalog, definition.Metadata)
	toolDefinition := api.ToolDefinition{
		Name: definition.Name, Description: definition.Description,
		InputSchema: maps.Clone(catalog.InputSchema), Group: catalog.Group,
		Parent: catalog.Parent, Icon: catalog.Icon, Strict: catalog.Strict,
		DefaultPermission: api.ToolMode(catalog.DefaultPermission),
		Handler: func(ctx context.Context, input map[string]any) (any, error) {
			return tool.RunRaw(ctx, input)
		},
	}
	return toolDefinition, catalog, nil
}

func cloneToolSet(set ToolSet) ToolSet {
	return ToolSet{
		Definitions: append([]api.ToolDefinition(nil), set.Definitions...),
		Catalog:     append([]aitools.ToolCatalogEntry(nil), set.Catalog...),
	}
}
