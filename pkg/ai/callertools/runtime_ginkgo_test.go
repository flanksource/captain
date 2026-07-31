package callertools_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Authenticated caller-tool runtime", func() {
	It("omits denied tools and rejects unauthenticated requests", func(ctx SpecContext) {
		var hiddenCalls atomic.Int32
		runtime, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{
				{
					Name: "invoice_get", Description: "Read an invoice",
					InputSchema:       map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
					DefaultPermission: api.ToolModeOn,
					Handler: func(_ context.Context, input map[string]any) (any, error) {
						return map[string]any{"id": input["id"], "status": "draft"}, nil
					},
				},
				{
					Name: "invoice_delete", DefaultPermission: api.ToolModeOn,
					Handler: func(context.Context, map[string]any) (any, error) {
						hiddenCalls.Add(1)
						return "deleted", nil
					},
				},
			},
			Preferences: api.ToolPreferences{"invoice_delete": api.ToolModeOff},
			SessionID:   "captain-session-1",
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(runtime.Close)

		response, err := http.Post(runtime.Endpoint().URL, "application/json", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(response.Body.Close()).To(Succeed())

		client := authenticatedClient(ctx, runtime.Endpoint())
		DeferCleanup(client.Close)
		tools, err := client.ListTools(ctx, mcp.ListToolsRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(tools.Tools).To(HaveLen(1))
		Expect(tools.Tools[0].Name).To(Equal("invoice_get"))

		request := mcp.CallToolRequest{}
		request.Params.Name = "invoice_delete"
		request.Params.Arguments = map[string]any{}
		_, err = client.CallTool(ctx, request)
		Expect(err).To(HaveOccurred())
		Expect(hiddenCalls.Load()).To(BeZero())
	})

	It("brokers ask tools and applies updated input", func(ctx SpecContext) {
		var calls atomic.Int32
		runtime, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{{
				Name: "invoice_update", DefaultPermission: api.ToolModeAsk,
				Handler: func(_ context.Context, input map[string]any) (any, error) {
					calls.Add(1)
					return input, nil
				},
			}},
			CanUseTool: func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
				Expect(request.Tool).To(Equal("invoice_update"))
				Expect(request.SessionID).To(Equal("captain-session-2"))
				Expect(request.ToolUseID).To(Equal("approval-call-1"))
				return api.PermissionDecision{Allow: true, UpdatedInput: map[string]any{"status": "approved"}}, nil
			},
			SessionID: "captain-session-2",
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(runtime.Close)

		client := authenticatedClient(ctx, runtime.Endpoint())
		DeferCleanup(client.Close)
		request := mcp.CallToolRequest{}
		request.Params.Name = "invoice_update"
		request.Params.Arguments = map[string]any{"status": "draft"}
		request.Params.Meta = &mcp.Meta{AdditionalFields: map[string]any{"toolUseId": "approval-call-1"}}
		result, err := client.CallTool(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeFalse())
		Expect(result.StructuredContent).To(Equal(map[string]any{"status": "approved"}))
		Expect(calls.Load()).To(Equal(int32(1)))
	})

	It("rejects wrong-session credentials and browser origins", func() {
		first := newRuntime("captain-session-1", "first")
		DeferCleanup(first.Close)
		second := newRuntime("captain-session-2", "second")
		DeferCleanup(second.Close)

		request, err := http.NewRequest(http.MethodPost, first.Endpoint().URL, nil)
		Expect(err).NotTo(HaveOccurred())
		for name, value := range second.Endpoint().Headers {
			request.Header.Set(name, value)
		}
		response, err := http.DefaultClient.Do(request)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(response.Body.Close()).To(Succeed())

		request, err = http.NewRequest(http.MethodPost, first.Endpoint().URL, nil)
		Expect(err).NotTo(HaveOccurred())
		for name, value := range first.Endpoint().Headers {
			request.Header.Set(name, value)
		}
		request.Header.Set("Origin", "https://example.com")
		response, err = http.DefaultClient.Do(request)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusForbidden))
		Expect(response.Body.Close()).To(Succeed())
	})

	It("expires and explicitly revokes capabilities", func() {
		expiring, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{{
				Name: "lookup", DefaultPermission: api.ToolModeOn,
				Handler: func(context.Context, map[string]any) (any, error) { return "ok", nil },
			}},
			SessionID: "expiring-session",
			ExpiresAt: time.Now().Add(25 * time.Millisecond),
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expiring.Close)
		Eventually(func() int {
			return authenticatedStatus(expiring.Endpoint())
		}).Should(Equal(http.StatusUnauthorized))

		revoked := newRuntime("revoked-session", "revoked")
		DeferCleanup(revoked.Close)
		revoked.Revoke()
		Expect(authenticatedStatus(revoked.Endpoint())).To(Equal(http.StatusUnauthorized))
	})

	It("times out approvals and returns handler failures without executing past the boundary", func(ctx SpecContext) {
		var calls atomic.Int32
		runtime, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{{
				Name: "invoice_update", DefaultPermission: api.ToolModeAsk,
				Handler: func(context.Context, map[string]any) (any, error) {
					calls.Add(1)
					return nil, errors.New("must not execute")
				},
			}},
			CanUseTool: func(ctx context.Context, _ api.PermissionRequest) (api.PermissionDecision, error) {
				<-ctx.Done()
				return api.PermissionDecision{}, ctx.Err()
			},
			SessionID:       "approval-session",
			ApprovalTimeout: 25 * time.Millisecond,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(runtime.Close)

		client := authenticatedClient(ctx, runtime.Endpoint())
		DeferCleanup(client.Close)
		request := mcp.CallToolRequest{}
		request.Params.Name = "invoice_update"
		result, err := client.CallTool(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeTrue())
		Expect(calls.Load()).To(BeZero())
	})

	It("returns handler failures as MCP tool errors", func(ctx SpecContext) {
		var calls atomic.Int32
		runtime, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{{
				Name: "invoice_get", DefaultPermission: api.ToolModeOn,
				Handler: func(context.Context, map[string]any) (any, error) {
					calls.Add(1)
					return nil, errors.New("invoice unavailable")
				},
			}},
			SessionID: "failure-session",
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(runtime.Close)

		client := authenticatedClient(ctx, runtime.Endpoint())
		DeferCleanup(client.Close)
		request := mcp.CallToolRequest{}
		request.Params.Name = "invoice_get"
		result, err := client.CallTool(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeTrue())
		Expect(calls.Load()).To(Equal(int32(1)))
	})

	It("rejects approval-updated input that violates the tool schema", func(ctx SpecContext) {
		var calls atomic.Int32
		runtime, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{{
				Name: "invoice_update", DefaultPermission: api.ToolModeAsk,
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "string"},
					},
					"required": []string{"id"},
				},
				Handler: func(context.Context, map[string]any) (any, error) {
					calls.Add(1)
					return "updated", nil
				},
			}},
			CanUseTool: func(context.Context, api.PermissionRequest) (api.PermissionDecision, error) {
				return api.PermissionDecision{Allow: true, UpdatedInput: map[string]any{"id": 42}}, nil
			},
			SessionID: "validation-session",
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(runtime.Close)

		client := authenticatedClient(ctx, runtime.Endpoint())
		DeferCleanup(client.Close)
		request := mcp.CallToolRequest{}
		request.Params.Name = "invoice_update"
		request.Params.Arguments = map[string]any{"id": "inv-1"}
		result, err := client.CallTool(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeTrue())
		Expect(calls.Load()).To(BeZero())
	})

	It("isolates concurrently active session capabilities", func(ctx SpecContext) {
		first := newRuntime("captain-session-1", "first")
		DeferCleanup(first.Close)
		second := newRuntime("captain-session-2", "second")
		DeferCleanup(second.Close)
		firstClient := authenticatedClient(ctx, first.Endpoint())
		DeferCleanup(firstClient.Close)
		secondClient := authenticatedClient(ctx, second.Endpoint())
		DeferCleanup(secondClient.Close)

		type outcome struct {
			result *mcp.CallToolResult
			err    error
		}
		outcomes := make(chan outcome, 2)
		call := func(client *mcpclient.Client) {
			request := mcp.CallToolRequest{}
			request.Params.Name = "identity"
			result, err := client.CallTool(ctx, request)
			outcomes <- outcome{result: result, err: err}
		}
		go call(firstClient)
		go call(secondClient)

		values := make([]string, 0, 2)
		for range 2 {
			outcome := <-outcomes
			Expect(outcome.err).NotTo(HaveOccurred())
			Expect(outcome.result.IsError).To(BeFalse())
			values = append(values, outcome.result.StructuredContent.(map[string]any)["session"].(string))
		}
		Expect(values).To(ConsistOf("first", "second"))
	})
})

func authenticatedClient(ctx context.Context, endpoint api.CallerToolEndpoint) *mcpclient.Client {
	channel, err := transport.NewStreamableHTTP(endpoint.URL, transport.WithHTTPHeaders(endpoint.Headers))
	Expect(err).NotTo(HaveOccurred())
	client := mcpclient.NewClient(channel)
	Expect(client.Start(ctx)).To(Succeed())
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: "captain-test", Version: "1.0.0"}
	_, err = client.Initialize(ctx, request)
	Expect(err).NotTo(HaveOccurred())
	return client
}

func authenticatedStatus(endpoint api.CallerToolEndpoint) int {
	request, err := http.NewRequest(http.MethodPost, endpoint.URL, nil)
	Expect(err).NotTo(HaveOccurred())
	for name, value := range endpoint.Headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	Expect(err).NotTo(HaveOccurred())
	defer func() {
		Expect(response.Body.Close()).To(Succeed())
	}()
	return response.StatusCode
}

func newRuntime(sessionID, marker string) *callertools.Runtime {
	runtime, err := callertools.New(callertools.Options{
		Definitions: []api.ToolDefinition{{
			Name: "identity", DefaultPermission: api.ToolModeOn,
			Handler: func(context.Context, map[string]any) (any, error) {
				return map[string]any{"session": marker}, nil
			},
		}},
		SessionID: sessionID,
	})
	Expect(err).NotTo(HaveOccurred())
	return runtime
}
