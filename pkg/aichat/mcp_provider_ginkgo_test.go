package aichat

import (
	"context"
	"errors"
	"fmt"

	genkitai "github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type fakeMCPClient struct {
	tools       []genkitai.Tool
	discoverErr error
	discoveries int
	disconnects int
	disconnect  error
}

func (f *fakeMCPClient) GetActiveTools(context.Context, *genkit.Genkit) ([]genkitai.Tool, error) {
	f.discoveries++
	return f.tools, f.discoverErr
}

func (f *fakeMCPClient) Disconnect() error {
	f.disconnects++
	return f.disconnect
}

var _ = Describe("Captain MCP tool provider", func() {
	It("caches explicit clients and projects executable Genkit tools", func(ctx SpecContext) {
		client := &fakeMCPClient{tools: []genkitai.Tool{newFakeMCPTool("weather_lookup")}}
		provider := NewMCPToolProvider(MCPToolProviderOptions{
			Servers: []MCPServer{{Name: "weather", URL: "https://example.com/mcp", StreamableHTTP: true}},
		})
		DeferCleanup(provider.Close)
		created := 0
		provider.clientFactory = func(options mcp.MCPClientOptions) (mcpClient, error) {
			created++
			Expect(options.Name).To(Equal("weather"))
			Expect(options.StreamableHTTP.BaseURL).To(Equal("https://example.com/mcp"))
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
		Expect(first.Definitions[0].Name).To(Equal("weather_lookup"))
		Expect(first.Catalog).To(HaveLen(1))
		Expect(first.Catalog[0].Source).To(Equal("mcp"))
		Expect(first.Catalog[0].Server).To(Equal("weather"))

		result, err := first.Definitions[0].Handler(ctx, map[string]any{"city": "Cape Town"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(map[string]any{"city": "Cape Town"}))
	})

	It("fails the whole load and disconnects clients when server tools collide", func(ctx SpecContext) {
		clients := []*fakeMCPClient{
			{tools: []genkitai.Tool{newFakeMCPTool("duplicate")}},
			{tools: []genkitai.Tool{newFakeMCPTool("duplicate")}},
		}
		provider := NewMCPToolProvider(MCPToolProviderOptions{Servers: []MCPServer{
			{Name: "first", Command: "first-server"},
			{Name: "second", Command: "second-server"},
		}})
		created := 0
		provider.clientFactory = func(mcp.MCPClientOptions) (mcpClient, error) {
			client := clients[created]
			created++
			return client, nil
		}

		_, err := provider.ToolSet(ctx)
		Expect(err).To(MatchError(ContainSubstring("duplicate MCP tool definition \"duplicate\"")))
		Expect(clients[0].disconnects).To(Equal(1))
		Expect(clients[1].disconnects).To(Equal(1))
	})

	It("reports the failing server and closes earlier clients", func(ctx SpecContext) {
		first := &fakeMCPClient{tools: []genkitai.Tool{newFakeMCPTool("first_tool")}}
		provider := NewMCPToolProvider(MCPToolProviderOptions{Servers: []MCPServer{
			{Name: "first", Command: "first-server"},
			{Name: "broken", Command: "broken-server"},
		}})
		provider.clientFactory = func(options mcp.MCPClientOptions) (mcpClient, error) {
			if options.Name == "broken" {
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
			"first":  {tools: []genkitai.Tool{newFakeMCPTool("first_tool")}},
			"broken": {discoverErr: errors.New("list failed")},
		}
		provider := NewMCPToolProvider(MCPToolProviderOptions{Servers: []MCPServer{
			{Name: "first", Command: "first-server"},
			{Name: "broken", Command: "broken-server"},
		}})
		provider.clientFactory = func(options mcp.MCPClientOptions) (mcpClient, error) {
			return clients[options.Name], nil
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
		provider.clientFactory = func(options mcp.MCPClientOptions) (mcpClient, error) {
			return clients[options.Name], nil
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

func newFakeMCPTool(name string) genkitai.Tool {
	return genkitai.NewTool(name, fmt.Sprintf("Run %s", name), func(_ *genkitai.ToolContext, input map[string]any) (map[string]any, error) {
		return input, nil
	})
}
