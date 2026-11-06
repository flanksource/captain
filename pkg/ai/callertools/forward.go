package callertools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/flanksource/captain/pkg/api"
)

// Forward lists the tools another caller-tool server serves and returns a
// definition per tool, under its own name, whose handler calls it there. The
// forwarded server keeps every decision: its tools arrive as allow here because
// it gates each call itself, and its approval broker is the one that asks. The
// closer ends the session.
func Forward(ctx context.Context, endpoint api.CallerToolEndpoint) ([]api.ToolDefinition, func() error, error) {
	channel, err := transport.NewStreamableHTTP(endpoint.URL, transport.WithHTTPHeaders(maps.Clone(endpoint.Headers)))
	if err != nil {
		return nil, nil, fmt.Errorf("forward caller tools: %w", err)
	}
	client := mcpclient.NewClient(channel)
	if err := client.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("forward caller tools: start: %w", err)
	}
	initialize := mcp.InitializeRequest{}
	initialize.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initialize.Params.ClientInfo = mcp.Implementation{Name: "captain-forwarder", Version: "1"}
	if _, err := client.Initialize(ctx, initialize); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("forward caller tools: initialize: %w", err), client.Close())
	}
	listed, err := client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("forward caller tools: list: %w", err), client.Close())
	}
	definitions := make([]api.ToolDefinition, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		definition, err := forwardedDefinition(client, tool)
		if err != nil {
			return nil, nil, errors.Join(err, client.Close())
		}
		definitions = append(definitions, definition)
	}
	return definitions, client.Close, nil
}

func forwardedDefinition(client *mcpclient.Client, tool mcp.Tool) (api.ToolDefinition, error) {
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return api.ToolDefinition{}, err
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return api.ToolDefinition{}, err
	}
	name := tool.Name
	return api.ToolDefinition{
		Name: name, Description: tool.Description, InputSchema: schema,
		ReadOnlyHint: tool.Annotations.ReadOnlyHint, DestructiveHint: tool.Annotations.DestructiveHint,
		DefaultPermission: api.ToolPolicyAllow,
		Handler: func(ctx context.Context, input map[string]any) (any, error) {
			request := mcp.CallToolRequest{}
			request.Params.Name = name
			request.Params.Arguments = input
			result, err := client.CallTool(ctx, request)
			if err != nil {
				return nil, fmt.Errorf("forwarded tool %s: %w", name, err)
			}
			return forwardedResult(name, result)
		},
	}, nil
}

// forwardedResult is the forwarded call's answer as a local handler gives one:
// its structured content when there is some, otherwise its text, and an error
// when the far side reported one.
func forwardedResult(name string, result *mcp.CallToolResult) (any, error) {
	var text []string
	for _, content := range result.Content {
		if textContent, ok := mcp.AsTextContent(content); ok {
			text = append(text, textContent.Text)
		}
	}
	if result.IsError {
		return nil, fmt.Errorf("forwarded tool %s: %s", name, strings.Join(text, "\n"))
	}
	if result.StructuredContent != nil {
		return result.StructuredContent, nil
	}
	return strings.Join(text, "\n"), nil
}
