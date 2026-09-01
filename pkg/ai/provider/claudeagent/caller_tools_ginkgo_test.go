package claudeagent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude Agent caller tools", func() {
	It("injects a request-scoped HTTP MCP endpoint", func() {
		provider := &Provider{
			model: "claude-sonnet-5",
			callerTools: &api.CallerToolEndpoint{
				Name: "captain", URL: "http://127.0.0.1:43210/mcp",
				Headers: map[string]string{"Authorization": "Bearer secret"},
			},
		}

		params, err := provider.initializeParams(ai.Request{Prompt: api.Prompt{User: "inspect"}})
		Expect(err).NotTo(HaveOccurred())

		Expect(params.MCPServers).To(HaveKey("captain"))
		Expect(params.MCPServers["captain"].Type).To(Equal("http"))
		Expect(params.MCPServers["captain"].URL).To(Equal("http://127.0.0.1:43210/mcp"))
		Expect(params.MCPServers["captain"].Headers).To(HaveKeyWithValue("Authorization", "Bearer secret"))
		raw, err := json.Marshal(params)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(ContainSubstring(`"callerToolUseIDKey":"__captain_tool_use_id"`))
	})

	It("executes an allowed caller tool through the fake Claude runtime", func(ctx SpecContext) {
		self, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		original := newAgentProcess
		newAgentProcess = func(*Provider) (*exec.Process, error) {
			return exec.NewExec(self).WithStdioPipe().WithEnv(map[string]string{
				fakeServerEnv: "1", fakeModeEnv: "caller-tools",
			}), nil
		}
		DeferCleanup(func() { newAgentProcess = original })

		var calls atomic.Int32
		permissions := make(chan api.PermissionRequest, 1)
		provider, err := New(ai.Config{
			Model:            api.Model{Name: "claude-sonnet-5"},
			CaptainSessionID: "captain-thread-1",
			CanUseTool: func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
				permissions <- request
				return api.PermissionDecision{Allow: true}, nil
			},
			Tools: []api.ToolDefinition{{
				Name: "invoice_get", DefaultPermission: api.ToolPolicyAsk,
				Handler: func(_ context.Context, input map[string]any) (any, error) {
					calls.Add(1)
					return map[string]any{"id": input["id"], "status": "draft"}, nil
				},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(provider.Close)

		events, err := provider.ExecuteStream(ctx, ai.Request{Prompt: api.Prompt{User: "inspect invoice"}})
		Expect(err).NotTo(HaveOccurred())
		var toolUse, toolResult api.Event
		for event := range events {
			switch event.Kind {
			case api.EventToolUse:
				toolUse = event
			case api.EventToolResult:
				toolResult = event
			}
		}
		Expect(calls.Load()).To(Equal(int32(1)))
		Expect(toolUse.Tool).To(Equal("invoice_get"))
		Expect(toolUse.ToolCallID).To(Equal("claude-tool-use-1"))
		var permission api.PermissionRequest
		Eventually(permissions).Should(Receive(&permission))
		Expect(permission.Tool).To(Equal(toolUse.Tool))
		Expect(permission.ToolUseID).To(Equal(toolUse.ToolCallID))
		Expect(permission.Input).To(Equal(map[string]any{"id": "inv-1"}))
		Expect(toolResult.ToolCallID).To(Equal(toolUse.ToolCallID))
		Expect(toolResult.Success).To(BeTrue())
	})

	It("binds the private capability to the Captain session identity", func() {
		provider, err := New(ai.Config{
			Model:            api.Model{Name: "claude-sonnet-5"},
			CaptainSessionID: "captain-thread-1",
			SessionID:        "provider-session-1",
			Tools: []api.ToolDefinition{{
				Name: "invoice_get", DefaultPermission: api.ToolPolicyAllow,
				Handler: func(context.Context, map[string]any) (any, error) { return "ok", nil },
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(provider.Close)

		Expect(provider.prepareCallerTools(ai.Request{SessionID: "provider-session-1"})).To(Succeed())
		Expect(provider.callerTools).NotTo(BeNil())
		Expect(provider.callerTools.Headers["Authorization"]).To(
			HavePrefix("Bearer cap_captain-thread-1."),
		)
		Expect(strings.Count(provider.callerTools.Headers["Authorization"], ".")).To(Equal(1))
	})

	It("does not require MCP when request preferences disable every caller tool", func() {
		provider, err := New(ai.Config{
			Tools: []api.ToolDefinition{{
				Name: "invoice_get", DefaultPermission: api.ToolPolicyAllow,
				Handler: func(context.Context, map[string]any) (any, error) { return "ok", nil },
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(provider.Close)

		request := ai.Request{
			ToolPreferences: api.ToolPreferences{"invoice_get": api.ToolPolicyDeny},
			Permissions:     api.Permissions{MCP: api.MCP{Disabled: true}},
		}
		Expect(provider.prepareCallerTools(request)).To(Succeed())
		Expect(provider.callerTools).To(BeNil())
	})
})
