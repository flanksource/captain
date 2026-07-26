package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

// Identity sent in the MCP initialize handshake. Servers log it, and some gate
// their capabilities on the client they are talking to.
const (
	mcpClientName    = "captain"
	mcpClientVersion = "1.0.0"
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

// mcpClient is the slice of an MCP session this provider needs: list the
// server's tools, invoke one, and shut the transport down.
type mcpClient interface {
	ListTools(context.Context) ([]mcp.Tool, error)
	CallTool(ctx context.Context, name string, arguments map[string]any) (any, error)
	Disconnect() error
}

type mcpClientFactory func(context.Context, MCPServer) (mcpClient, error)

// MCPToolProvider owns explicit MCP client sessions and their projected Captain
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
		options:       options,
		clientFactory: dialMCPServer,
		clients:       map[string]mcpClient{},
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
		client, err := p.client(ctx, server)
		if err != nil {
			return ToolSet{}, fmt.Errorf("connect MCP server %q: %w", server.Name, err)
		}
		tools, err := client.ListTools(ctx)
		if err != nil {
			return ToolSet{}, fmt.Errorf("discover MCP tools from server %q: %w", server.Name, err)
		}
		for _, tool := range tools {
			name := namespacedToolName(server.Name, tool.Name)
			if prior, exists := seen[name]; exists {
				return ToolSet{}, fmt.Errorf("duplicate MCP tool definition %q from servers %q and %q", name, prior, server.Name)
			}
			seen[name] = server.Name
			definition, catalog, err := projectMCPTool(server.Name, tool, client)
			if err != nil {
				return ToolSet{}, err
			}
			set.Definitions = append(set.Definitions, definition)
			set.Catalog = append(set.Catalog, catalog)
		}
	}
	return set, nil
}

func (p *MCPToolProvider) client(ctx context.Context, server MCPServer) (mcpClient, error) {
	if client := p.clients[server.Name]; client != nil {
		return client, nil
	}
	client, err := p.clientFactory(ctx, server)
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

// dialMCPServer opens and initializes one MCP session. The handshake happens
// here rather than lazily on first use: a server that cannot initialize has no
// tools to publish, and failing at connect time attributes the failure to the
// server that caused it.
func dialMCPServer(ctx context.Context, server MCPServer) (mcpClient, error) {
	channel, err := mcpTransport(server)
	if err != nil {
		return nil, err
	}
	client := mcpclient.NewClient(channel)
	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("start transport: %w", err)
	}
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: mcpClientName, Version: mcpClientVersion}
	if _, err := client.Initialize(ctx, request); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize session: %w", err), client.Close())
	}
	return &mcpSession{client: client}, nil
}

func mcpTransport(server MCPServer) (transport.Interface, error) {
	switch {
	case server.Command != "":
		return transport.NewStdio(server.Command, append([]string(nil), server.Env...), server.Args...), nil
	case server.StreamableHTTP:
		return transport.NewStreamableHTTP(server.URL, transport.WithHTTPHeaders(maps.Clone(server.Headers)))
	default:
		return transport.NewSSE(server.URL, transport.WithHeaders(maps.Clone(server.Headers)))
	}
}

// mcpSession adapts an mcp-go client to the narrow surface this provider uses.
type mcpSession struct {
	client *mcpclient.Client
}

func (s *mcpSession) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	result, err := s.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func (s *mcpSession) CallTool(ctx context.Context, name string, arguments map[string]any) (any, error) {
	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = arguments
	result, err := s.client.CallTool(ctx, request)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *mcpSession) Disconnect() error { return s.client.Close() }

// namespacedToolName scopes a server's tool to that server, so two servers
// exposing "search" stay distinguishable. The name is also the catalog's
// preference key, so it is part of the stored contract, not cosmetic.
func namespacedToolName(server, tool string) string {
	return fmt.Sprintf("%s_%s", server, tool)
}

func projectMCPTool(server string, tool mcp.Tool, client mcpClient) (api.ToolDefinition, aitools.ToolCatalogEntry, error) {
	name := namespacedToolName(server, tool.Name)
	inputSchema, err := argumentsSchema(mcp.ToolArgumentsSchema(tool.InputSchema))
	if err != nil {
		return api.ToolDefinition{}, aitools.ToolCatalogEntry{}, fmt.Errorf("read input schema of MCP tool %q from server %q: %w", name, server, err)
	}
	outputSchema, err := argumentsSchema(mcp.ToolArgumentsSchema(tool.OutputSchema))
	if err != nil {
		return api.ToolDefinition{}, aitools.ToolCatalogEntry{}, fmt.Errorf("read output schema of MCP tool %q from server %q: %w", name, server, err)
	}
	title := tool.Title
	if title == "" {
		title = name
	}
	catalog := aitools.ToolCatalogEntry{
		Name: name, Title: title, Description: tool.Description,
		Source: "mcp", Server: server, PreferenceKey: name,
		DefaultPermission: api.ToolModeAuto, InputSchema: aitools.ObjectSchema(inputSchema),
		OutputSchema: outputSchema,
	}
	// _meta is where an MCP server publishes the grouping, icon and permission
	// hints the catalog understands.
	if tool.Meta != nil {
		aitools.ApplyToolMetadata(&catalog, tool.Meta.AdditionalFields)
	}
	required := append([]string(nil), tool.InputSchema.Required...)
	definition := api.ToolDefinition{
		Name: name, Description: tool.Description,
		InputSchema: maps.Clone(catalog.InputSchema), Group: catalog.Group,
		Parent: catalog.Parent, Icon: catalog.Icon, Strict: catalog.Strict,
		DefaultPermission: api.ToolMode(catalog.DefaultPermission),
		Handler: func(ctx context.Context, input map[string]any) (any, error) {
			for _, field := range required {
				if _, present := input[field]; !present {
					return nil, fmt.Errorf("MCP tool %q requires field %q", name, field)
				}
			}
			return client.CallTool(ctx, tool.Name, input)
		},
	}
	return definition, catalog, nil
}

// argumentsSchema renders an mcp-go schema as the plain JSON Schema map the
// catalog stores, and nil when the server declared no schema at all — the
// zero struct marshals to `{"type":"","properties":null}`, which is not one.
func argumentsSchema(schema mcp.ToolArgumentsSchema) (map[string]any, error) {
	if schema.Type == "" && schema.Properties == nil && schema.Defs == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func cloneToolSet(set ToolSet) ToolSet {
	return ToolSet{
		Definitions: append([]api.ToolDefinition(nil), set.Definitions...),
		Catalog:     append([]aitools.ToolCatalogEntry(nil), set.Catalog...),
	}
}
