package provider

import (
	"context"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex Agent caller tools", func() {
	It("injects the same request-scoped MCP endpoint on start and resume", func() {
		endpoint := &api.CallerToolEndpoint{
			Name: "captain", URL: "http://127.0.0.1:43210/mcp",
			Headers: map[string]string{"Authorization": "Bearer secret"},
		}
		request := ai.Request{
			SessionID: "thread-1",
			Prompt:    api.Prompt{User: "inspect"},
		}

		start, err := buildThreadStartParams("gpt-5.4", request, endpoint)
		Expect(err).NotTo(HaveOccurred())
		for _, params := range []map[string]any{start, buildResumeParams(request, endpoint)} {
			config, ok := params["config"].(map[string]any)
			Expect(ok).To(BeTrue())
			servers, ok := config["mcp_servers"].(map[string]any)
			Expect(ok).To(BeTrue())
			serverConfig, ok := servers["captain"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(serverConfig).To(HaveKeyWithValue("url", endpoint.URL))
			Expect(serverConfig).To(HaveKeyWithValue("http_headers", endpoint.Headers))
			Expect(serverConfig).To(HaveKeyWithValue("required", true))
		}
	})

	It("binds the private capability to the Captain session identity", func() {
		provider, err := NewCodexAppServer(ai.Config{
			Model:            api.Model{Name: "gpt-5.4"},
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
		provider, err := NewCodexAppServer(ai.Config{
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
