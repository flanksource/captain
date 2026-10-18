package callertools_test

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/mark3labs/mcp-go/mcp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type claudeMCPConfig struct {
	MCPServers map[string]struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	} `json:"mcpServers"`
}

var _ = Describe("Caller tools for a CLI run", func() {
	const (
		allowTool = "invoice_get"
		askTool   = "invoice_update"
		denyTool  = "invoice_delete"
		answer    = "invoice 42"
	)

	start := func(ctx context.Context, onApproval api.ApprovalFunc) *callertools.CLIRun {
		handler := func(context.Context, map[string]any) (any, error) { return answer, nil }
		run, err := callertools.StartCLI(ctx, api.Config{
			Tools: []api.ToolDefinition{
				{Name: allowTool, DefaultPermission: api.ToolPolicyAllow, Handler: handler},
				{Name: askTool, DefaultPermission: api.ToolPolicyAsk, Handler: handler},
				{Name: denyTool, DefaultPermission: api.ToolPolicyAllow, Handler: handler},
			},
			OnApproval:       onApproval,
			CaptainSessionID: "session-1",
		}, api.Spec{ToolPreferences: api.ToolPreferences{denyTool: api.ToolPolicyDeny}})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		return run
	}

	It("pre-approves every served tool in claude and omits denied ones", func(ctx SpecContext) {
		run := start(ctx, nil)
		Expect(run.ClaudeAllowedTools()).To(Equal([]string{"mcp__captain__" + allowTool, "mcp__captain__" + askTool}))
	}, SpecTimeout(10*time.Second))

	It("serves tools/call through the endpoint its claude MCP config file names, asking OnApproval for ask tools", func(ctx SpecContext) {
		var asked []api.ApprovalRequest
		run := start(ctx, func(_ context.Context, req api.ApprovalRequest) (api.ApprovalDecision, error) {
			asked = append(asked, req)
			return api.ApprovalDecision{Allow: true}, nil
		})
		path, err := run.ClaudeMCPConfigFile()
		Expect(err).NotTo(HaveOccurred())
		info, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		raw, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var config claudeMCPConfig
		Expect(json.Unmarshal(raw, &config)).To(Succeed())
		server := config.MCPServers["captain"]
		Expect(server.Type).To(Equal("http"))

		client := authenticatedClient(ctx, api.CallerToolEndpoint{Name: "captain", URL: server.URL, Headers: server.Headers})
		DeferCleanup(client.Close)
		for _, name := range []string{allowTool, askTool} {
			request := mcp.CallToolRequest{}
			request.Params.Name = name
			result, err := client.CallTool(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(toolOutput(result)).To(Equal(toolOutputOf{Text: answer}))
		}
		Expect(asked).To(HaveLen(1))
		Expect(asked[0]).To(approvalIdentity(askTool, api.ApprovalKindTool, "session-1"))

		Expect(run.Close()).To(Succeed())
		_, err = os.Stat(path)
		Expect(os.IsNotExist(err)).To(BeTrue(), "Close removes the credential file")
	}, SpecTimeout(10*time.Second))

	It("registers the server with codex through env-backed headers, never the bearer value", func(ctx SpecContext) {
		run := start(ctx, nil)
		merged, err := run.CodexConfig(false)
		Expect(err).NotTo(HaveOccurred())
		replaced, err := run.CodexConfig(true)
		Expect(err).NotTo(HaveOccurred())
		env := run.CodexEnv()
		Expect(env).To(HaveLen(1))
		Expect(env[0]).To(HavePrefix("CAPTAIN_CALLER_TOOLS_AUTHORIZATION=Bearer cap_session-1."))
		table := `{url="` + endpointURL(run) + `",env_http_headers={"Authorization"="CAPTAIN_CALLER_TOOLS_AUTHORIZATION"},required=true,enabled=true,default_tools_approval_mode="approve"}`
		Expect(merged).To(Equal("mcp_servers.captain=" + table))
		Expect(replaced).To(Equal("mcp_servers={captain=" + table + "}"))
	}, SpecTimeout(10*time.Second))

	It("serves nothing when every caller tool is denied", func(ctx SpecContext) {
		run, err := callertools.StartCLI(ctx, api.Config{
			Tools: []api.ToolDefinition{{Name: denyTool, DefaultPermission: api.ToolPolicyDeny,
				Handler: func(context.Context, map[string]any) (any, error) { return answer, nil }}},
		}, api.Spec{})
		Expect(err).NotTo(HaveOccurred())
		Expect(run).To(BeNil())
	})
})

// approvalIdentity checks the approval identity a CLI caller-tool call carries.
func approvalIdentity(tool string, kind api.ApprovalKind, sessionID string) OmegaMatcher {
	return And(
		WithTransform(func(r api.ApprovalRequest) string { return r.Tool }, Equal(tool)),
		WithTransform(func(r api.ApprovalRequest) api.ApprovalKind { return r.Kind }, Equal(kind)),
		WithTransform(func(r api.ApprovalRequest) string { return r.SessionID }, Equal(sessionID)),
	)
}

func endpointURL(run *callertools.CLIRun) string {
	path, err := run.ClaudeMCPConfigFile()
	Expect(err).NotTo(HaveOccurred())
	raw, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	var config claudeMCPConfig
	Expect(json.Unmarshal(raw, &config)).To(Succeed())
	return config.MCPServers["captain"].URL
}
