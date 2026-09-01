package callertools_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("Authenticated caller-tool runtime", func() {
	It("omits denied tools and rejects unauthenticated requests", func(ctx SpecContext) {
		var hiddenCalls atomic.Int32
		runtime, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{
				{
					Name: "invoice_get", Description: "Read an invoice",
					InputSchema:       map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
					DefaultPermission: api.ToolPolicyAllow,
					Handler: func(_ context.Context, input map[string]any) (any, error) {
						return map[string]any{"id": input["id"], "status": "draft"}, nil
					},
				},
				{
					Name: "invoice_delete", DefaultPermission: api.ToolPolicyAllow,
					Handler: func(context.Context, map[string]any) (any, error) {
						hiddenCalls.Add(1)
						return "deleted", nil
					},
				},
			},
			Preferences: api.ToolPreferences{"invoice_delete": api.ToolPolicyDeny},
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
				Name: "invoice_update", DefaultPermission: api.ToolPolicyAsk,
				Handler: func(_ context.Context, input map[string]any) (any, error) {
					calls.Add(1)
					return input, nil
				},
			}},
			CanUseTool: func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
				Expect(request.Tool).To(Equal("invoice_update"))
				Expect(request.SessionID).To(Equal("captain-session-2"))
				Expect(request.ToolUseID).To(Equal("approval-call-1"))
				Expect(request.Delegated).To(BeFalse())
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

	It("uses the provider tool-use ID without exposing transport input to the handler", func(ctx SpecContext) {
		var handledInput map[string]any
		permissionRequests := make(chan api.PermissionRequest, 1)
		runtime, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{{
				Name: "invoice_update", DefaultPermission: api.ToolPolicyAsk,
				Handler: func(_ context.Context, input map[string]any) (any, error) {
					handledInput = input
					return input, nil
				},
			}},
			CanUseTool: func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
				permissionRequests <- request
				return api.PermissionDecision{Allow: true}, nil
			},
			SessionID: "provider-correlation-session",
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(runtime.Close)

		client := authenticatedClient(ctx, runtime.Endpoint())
		DeferCleanup(client.Close)
		request := mcp.CallToolRequest{}
		request.Params.Name = "invoice_update"
		request.Params.Arguments = map[string]any{
			"status": "draft", "__captain_tool_use_id": "claude-tool-use-1",
		}
		result, err := client.CallTool(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeFalse())
		var permission api.PermissionRequest
		Eventually(permissionRequests).Should(Receive(&permission))
		Expect(permission.ToolUseID).To(Equal("claude-tool-use-1"))
		Expect(permission.Input).To(Equal(map[string]any{"status": "draft"}))
		Expect(handledInput).To(Equal(map[string]any{"status": "draft"}))
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
				Name: "lookup", DefaultPermission: api.ToolPolicyAllow,
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
				Name: "invoice_update", DefaultPermission: api.ToolPolicyAsk,
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
				Name: "invoice_get", DefaultPermission: api.ToolPolicyAllow,
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
				Name: "invoice_update", DefaultPermission: api.ToolPolicyAsk,
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

	It("exposes and executes only the tools selected for a remote task", func(ctx SpecContext) {
		remote := httptest.NewServer(callertools.RemoteHandler())
		DeferCleanup(remote.Close)
		var hiddenCalls atomic.Int32
		runtime, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{
				{
					Name: "version", DefaultPermission: api.ToolPolicyAllow,
					Handler: func(context.Context, map[string]any) (any, error) {
						return map[string]any{"version": "test"}, nil
					},
				},
				{
					Name: "contexts", DefaultPermission: api.ToolPolicyAllow,
					Handler: func(context.Context, map[string]any) (any, error) { return []string{"default"}, nil },
				},
				{
					Name: "whoami", DefaultPermission: api.ToolPolicyAllow,
					Handler: func(context.Context, map[string]any) (any, error) {
						hiddenCalls.Add(1)
						return "captain", nil
					},
				},
			},
			SessionID: "remote-session",
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(runtime.Close)

		delegation, err := runtime.Endpoint().Delegate(ctx, api.CallerToolBinding{
			TaskID: "task-1", Agent: "agent-1", ExpiresAt: time.Now().Add(time.Minute),
			ToolNames: []string{"version", "contexts"},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(delegation.Revoke)
		client := authenticatedClient(ctx, servedDelegation(remote.URL, delegation.Endpoint))
		DeferCleanup(client.Close)

		tools, err := client.ListTools(ctx, mcp.ListToolsRequest{})
		Expect(err).NotTo(HaveOccurred())
		names := make([]string, 0, len(tools.Tools))
		for _, tool := range tools.Tools {
			names = append(names, tool.Name)
		}
		Expect(names).To(ConsistOf("version", "contexts"))

		request := mcp.CallToolRequest{}
		request.Params.Name = "version"
		result, err := client.CallTool(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeFalse())
		Expect(result.StructuredContent).To(Equal(map[string]any{"version": "test"}))

		request.Params.Name = "whoami"
		_, err = client.CallTool(ctx, request)
		Expect(err).To(HaveOccurred())
		Expect(hiddenCalls.Load()).To(BeZero())
	})

	It("rejects remote credentials with the wrong bearer or binding and after expiry or revocation", func(ctx SpecContext) {
		remote := httptest.NewServer(callertools.RemoteHandler())
		DeferCleanup(remote.Close)
		runtime := newRuntime("remote-auth-session", "remote")
		DeferCleanup(runtime.Close)
		issue := func(expiry time.Time) *api.CallerToolDelegation {
			delegation, err := runtime.Endpoint().Delegate(ctx, api.CallerToolBinding{
				TaskID: "task-auth", Agent: "agent-auth", ExpiresAt: expiry, ToolNames: []string{"identity"},
			})
			Expect(err).NotTo(HaveOccurred())
			delegation.Endpoint = servedDelegation(remote.URL, delegation.Endpoint)
			return delegation
		}

		active := issue(time.Now().Add(time.Minute))
		DeferCleanup(active.Revoke)
		invalidBearer := cloneEndpoint(active.Endpoint)
		invalidBearer.Headers["Authorization"] = "Bearer invalid"
		Expect(authenticatedStatus(invalidBearer)).To(Equal(http.StatusUnauthorized))
		wrongTask := cloneEndpoint(active.Endpoint)
		wrongTask.Headers[callertools.TaskHeader] = "another-task"
		Expect(authenticatedStatus(wrongTask)).To(Equal(http.StatusForbidden))
		wrongAgent := cloneEndpoint(active.Endpoint)
		wrongAgent.Headers[callertools.AgentHeader] = "another-agent"
		Expect(authenticatedStatus(wrongAgent)).To(Equal(http.StatusForbidden))

		expiring := issue(time.Now().Add(25 * time.Millisecond))
		Eventually(func() int { return authenticatedStatus(expiring.Endpoint) }).Should(Equal(http.StatusUnauthorized))
		revoked := issue(time.Now().Add(time.Minute))
		revoked.Revoke()
		Expect(authenticatedStatus(revoked.Endpoint)).To(Equal(http.StatusUnauthorized))
	})

	It("returns terminal remote tool results for approval denial and broker failure", func(ctx SpecContext) {
		remote := httptest.NewServer(callertools.RemoteHandler())
		DeferCleanup(remote.Close)
		for _, test := range []struct {
			name     string
			decision api.PermissionDecision
			err      error
			message  string
			reason   string
		}{
			{name: "denied", decision: api.PermissionDecision{Message: "operator denied the call"}, message: "operator denied the call", reason: "approval_denied"},
			{name: "failed", err: errors.New("approval service unavailable"), message: "approval service unavailable", reason: "approval_failed"},
		} {
			var calls atomic.Int32
			events := make(chan api.Event, 2)
			audits := make(chan callertools.AuditEvent, 4)
			runtime, err := callertools.New(callertools.Options{
				Definitions: []api.ToolDefinition{{
					Name: "version", DefaultPermission: api.ToolPolicyAsk,
					Handler: func(context.Context, map[string]any) (any, error) {
						calls.Add(1)
						return "must not execute", nil
					},
				}},
				SessionID: "remote-approval-" + test.name,
				CanUseTool: func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
					Expect(request.Delegated).To(BeTrue())
					Expect(request.ToolUseIDGenerated).To(BeTrue())
					return test.decision, test.err
				},
				ObserveDelegatedTool: func(_ context.Context, event api.Event) error {
					events <- event
					return nil
				},
				Audit: func(event callertools.AuditEvent) { audits <- event },
			})
			Expect(err).NotTo(HaveOccurred())
			delegation, err := runtime.Endpoint().Delegate(ctx, api.CallerToolBinding{
				TaskID: "task-" + test.name, Agent: "agent-1", ExpiresAt: time.Now().Add(time.Minute),
				ToolNames: []string{"version"},
			})
			Expect(err).NotTo(HaveOccurred())
			client := authenticatedClient(ctx, servedDelegation(remote.URL, delegation.Endpoint))

			request := mcp.CallToolRequest{}
			request.Params.Name = "version"
			result, err := client.CallTool(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeTrue())
			Expect(toolResultText(result)).To(ContainSubstring(test.message))
			Expect(calls.Load()).To(BeZero())
			var use, terminal api.Event
			Eventually(events).Should(Receive(&use))
			Eventually(events).Should(Receive(&terminal))
			Expect(use.Kind).To(Equal(api.EventToolUse))
			Expect(terminal).To(MatchFields(IgnoreExtras, Fields{
				"Kind": Equal(api.EventToolResult), "ToolCallID": Equal(use.ToolCallID),
				"Success": BeFalse(), "Text": ContainSubstring(test.message), "Delegated": BeTrue(),
			}))
			Eventually(audits).Should(Receive(MatchFields(IgnoreExtras, Fields{
				"Action": Equal("call"), "Result": Equal("denied"), "Reason": Equal(test.reason),
			})))

			Expect(client.Close()).To(Succeed())
			delegation.Revoke()
			Expect(runtime.Close()).To(Succeed())
		}
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

func servedDelegation(serverURL string, endpoint api.CallerToolEndpoint) api.CallerToolEndpoint {
	parsed, err := url.Parse(endpoint.URL)
	Expect(err).NotTo(HaveOccurred())
	endpoint.URL = serverURL + parsed.RequestURI()
	return endpoint
}

func cloneEndpoint(endpoint api.CallerToolEndpoint) api.CallerToolEndpoint {
	cloned := endpoint
	cloned.Headers = make(map[string]string, len(endpoint.Headers))
	for name, value := range endpoint.Headers {
		cloned.Headers[name] = value
	}
	return cloned
}

func toolResultText(result *mcp.CallToolResult) string {
	Expect(result.Content).NotTo(BeEmpty())
	text, ok := mcp.AsTextContent(result.Content[0])
	Expect(ok).To(BeTrue())
	return text.Text
}

func newRuntime(sessionID, marker string) *callertools.Runtime {
	runtime, err := callertools.New(callertools.Options{
		Definitions: []api.ToolDefinition{{
			Name: "identity", DefaultPermission: api.ToolPolicyAllow,
			Handler: func(context.Context, map[string]any) (any, error) {
				return map[string]any{"session": marker}, nil
			},
		}},
		SessionID: sessionID,
	})
	Expect(err).NotTo(HaveOccurred())
	return runtime
}
