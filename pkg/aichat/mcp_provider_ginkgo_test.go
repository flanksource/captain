package aichat

import (
	"context"
	"errors"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type fakeMCPClient struct {
	tools       []mcp.Tool
	discoverErr error
	discoveries int
	disconnects int
	disconnect  error
	calls       []map[string]any
}

func (f *fakeMCPClient) ListTools(context.Context) ([]mcp.Tool, error) {
	f.discoveries++
	return f.tools, f.discoverErr
}

func (f *fakeMCPClient) CallTool(_ context.Context, name string, arguments map[string]any) (any, error) {
	f.calls = append(f.calls, arguments)
	return map[string]any{"tool": name, "arguments": arguments}, nil
}

func (f *fakeMCPClient) Disconnect() error {
	f.disconnects++
	return f.disconnect
}

var _ = Describe("Captain MCP tool provider", func() {
	It("caches explicit clients and projects executable, server-scoped tools", func(ctx SpecContext) {
		client := &fakeMCPClient{tools: []mcp.Tool{newFakeMCPTool("lookup")}}
		provider := NewMCPToolProvider(MCPToolProviderOptions{
			Servers: []MCPServer{{Name: "weather", URL: "https://example.com/mcp", StreamableHTTP: true}},
		})
		DeferCleanup(provider.Close)
		created := 0
		provider.clientFactory = func(_ context.Context, server MCPServer) (mcpClient, error) {
			created++
			Expect(server.Name).To(Equal("weather"))
			Expect(server.URL).To(Equal("https://example.com/mcp"))
			Expect(server.StreamableHTTP).To(BeTrue())
			return client, nil
		}

		first, err := provider.ToolSet(ctx)
		Expect(err).NotTo(HaveOccurred())
		second, err := provider.ToolSet(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(Equal(1))
		Expect(client.discoveries).To(Equal(1))
		Expect(second.Definitions).To(HaveLen(1))
		Expect(second.Definitions[0].Name).To(Equal(first.Definitions[0].Name))
		Expect(second.Catalog).To(Equal(first.Catalog))
		Expect(first.Definitions).To(HaveLen(1))
		// The server name scopes the tool, and the same string is the stored
		// preference key, so it may not drift.
		Expect(first.Definitions[0].Name).To(Equal("weather_lookup"))
		Expect(first.Catalog).To(HaveLen(1))
		Expect(first.Catalog[0].Source).To(Equal("mcp"))
		Expect(first.Catalog[0].Server).To(Equal("weather"))
		Expect(first.Catalog[0].PreferenceKey).To(Equal("weather_lookup"))
		Expect(first.Catalog[0].InputSchema).To(HaveKeyWithValue("type", "object"))

		// The server is called with its own unscoped name, not the scoped one.
		result, err := first.Definitions[0].Handler(ctx, map[string]any{"city": "Cape Town"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(map[string]any{
			"tool":      "lookup",
			"arguments": map[string]any{"city": "Cape Town"},
		}))
	})

	It("rejects a call that omits a field the server declared required", func(ctx SpecContext) {
		tool := newFakeMCPTool("lookup")
		tool.InputSchema.Required = []string{"city"}
		provider := NewMCPToolProvider(MCPToolProviderOptions{
			Servers: []MCPServer{{Name: "weather", Command: "weather-server"}},
		})
		DeferCleanup(provider.Close)
		client := &fakeMCPClient{tools: []mcp.Tool{tool}}
		provider.clientFactory = func(context.Context, MCPServer) (mcpClient, error) { return client, nil }

		set, err := provider.ToolSet(ctx)
		Expect(err).NotTo(HaveOccurred())

		_, err = set.Definitions[0].Handler(ctx, map[string]any{"country": "ZA"})
		Expect(err).To(MatchError(`MCP tool "weather_lookup" requires field "city"`))
		Expect(client.calls).To(BeEmpty(), "a call missing a required field must not reach the server")
	})

	It("fails the whole load and disconnects clients when scoped tool names collide", func(ctx SpecContext) {
		// Scoping joins server and tool with an underscore, so a server whose
		// name is a prefix of another's can still collide. That ambiguity is
		// the only way two distinct servers can produce one name.
		clients := []*fakeMCPClient{
			{tools: []mcp.Tool{newFakeMCPTool("b_c")}},
			{tools: []mcp.Tool{newFakeMCPTool("c")}},
		}
		provider := NewMCPToolProvider(MCPToolProviderOptions{Servers: []MCPServer{
			{Name: "a", Command: "first-server"},
			{Name: "a_b", Command: "second-server"},
		}})
		created := 0
		provider.clientFactory = func(context.Context, MCPServer) (mcpClient, error) {
			client := clients[created]
			created++
			return client, nil
		}

		_, err := provider.ToolSet(ctx)
		Expect(err).To(MatchError(ContainSubstring(`duplicate MCP tool definition "a_b_c" from servers "a" and "a_b"`)))
		Expect(clients[0].disconnects).To(Equal(1))
		Expect(clients[1].disconnects).To(Equal(1))
	})

	It("reports the failing server and closes earlier clients", func(ctx SpecContext) {
		first := &fakeMCPClient{tools: []mcp.Tool{newFakeMCPTool("first_tool")}}
		provider := NewMCPToolProvider(MCPToolProviderOptions{Servers: []MCPServer{
			{Name: "first", Command: "first-server"},
			{Name: "broken", Command: "broken-server"},
		}})
		provider.clientFactory = func(_ context.Context, server MCPServer) (mcpClient, error) {
			if server.Name == "broken" {
				return nil, errors.New("connection refused")
			}
			return first, nil
		}

		_, err := provider.ToolSet(ctx)
		Expect(err).To(MatchError(ContainSubstring(`connect MCP server "broken": connection refused`)))
		Expect(first.disconnects).To(Equal(1))
	})

	It("reports discovery failures and deterministically closes every client", func(ctx SpecContext) {
		clients := map[string]*fakeMCPClient{
			"first":  {tools: []mcp.Tool{newFakeMCPTool("first_tool")}},
			"broken": {discoverErr: errors.New("list failed")},
		}
		provider := NewMCPToolProvider(MCPToolProviderOptions{Servers: []MCPServer{
			{Name: "first", Command: "first-server"},
			{Name: "broken", Command: "broken-server"},
		}})
		provider.clientFactory = func(_ context.Context, server MCPServer) (mcpClient, error) {
			return clients[server.Name], nil
		}

		_, err := provider.ToolSet(ctx)
		Expect(err).To(MatchError(ContainSubstring(`discover MCP tools from server "broken": list failed`)))
		Expect(clients["first"].disconnects).To(Equal(1))
		Expect(clients["broken"].disconnects).To(Equal(1))

		Expect(provider.Close()).To(Succeed())
		Expect(clients["first"].disconnects).To(Equal(1))
		Expect(clients["broken"].disconnects).To(Equal(1))
	})

	It("returns every disconnect error with its server name", func(ctx SpecContext) {
		clients := map[string]*fakeMCPClient{
			"first":  {disconnect: errors.New("first close")},
			"second": {disconnect: errors.New("second close")},
		}
		provider := NewMCPToolProvider(MCPToolProviderOptions{Servers: []MCPServer{
			{Name: "first", Command: "first-server"},
			{Name: "second", Command: "second-server"},
		}})
		provider.clientFactory = func(_ context.Context, server MCPServer) (mcpClient, error) {
			return clients[server.Name], nil
		}
		_, err := provider.ToolSet(ctx)
		Expect(err).NotTo(HaveOccurred())

		err = provider.Close()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`disconnect MCP server "first": first close`))
		Expect(err.Error()).To(ContainSubstring(`disconnect MCP server "second": second close`))
		Expect(clients["first"].disconnects).To(Equal(1))
		Expect(clients["second"].disconnects).To(Equal(1))
	})
})

func newFakeMCPTool(name string) mcp.Tool {
	return mcp.Tool{
		Name:        name,
		Description: "Run " + name,
		InputSchema: mcp.ToolInputSchema{Type: "object", Properties: map[string]any{}},
	}
}
