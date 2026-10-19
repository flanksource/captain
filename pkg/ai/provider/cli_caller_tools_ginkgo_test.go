package provider

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CLI caller tools", func() {
	const toolName = "invoice_get"

	startCallerTools := func(ctx context.Context) *callertools.CLIRun {
		run, err := callertools.StartCLI(ctx, ai.Config{Tools: []api.ToolDefinition{{
			Name: toolName, DefaultPermission: api.ToolPolicyAllow,
			Handler: func(context.Context, map[string]any) (any, error) { return "ok", nil },
		}}}, ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(run).NotTo(BeNil())
		DeferCleanup(run.Close)
		return run
	}
	flagValue := func(args []string, flag string) string {
		index := slices.Index(args, flag)
		Expect(index).To(BeNumerically(">=", 0), "%s missing from %v", flag, args)
		return args[index+1]
	}
	bearer := func(run *callertools.CLIRun) string {
		_, value, _ := strings.Cut(run.CodexEnv()[0], "=")
		return value
	}

	DescribeTable("claude-cli registers the caller-tool server and pre-approves its tools",
		func(ctx SpecContext, mcpDisabled bool) {
			run := startCallerTools(ctx)
			req := ai.Request{Permissions: api.Permissions{
				Tools: api.Tools{"Read": api.ToolPolicyAllow},
				MCP:   api.MCP{Disabled: mcpDisabled},
			}}
			args, cleanup, err := buildClaudeCLIArgsWithMCP("claude-sonnet-5", req, nil, run)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(cleanup)

			Expect(strings.Split(flagValue(args, "--allowedTools"), ",")).To(ContainElements("Read", "mcp__captain__"+toolName))
			raw, err := os.ReadFile(flagValue(args, "--mcp-config"))
			Expect(err).NotTo(HaveOccurred())
			var config map[string]map[string]map[string]any
			Expect(json.Unmarshal(raw, &config)).To(Succeed())
			Expect(config["mcpServers"]).To(HaveKey("captain"))
			Expect(slices.Contains(args, "--strict-mcp-config")).To(Equal(mcpDisabled))
			Expect(strings.Join(args, " ")).NotTo(ContainSubstring(bearer(run)), "the credential stays out of argv")
		},
		Entry("alongside ambient servers", false),
		Entry("as the only server under mcp.disabled", true),
	)

	DescribeTable("codex-cli registers the caller-tool server through -c",
		func(ctx SpecContext, mcpDisabled bool, prefix string) {
			run := startCallerTools(ctx)
			req := ai.Request{Permissions: api.Permissions{MCP: api.MCP{Disabled: mcpDisabled}}}
			args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5", CallerTools: run}, req)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(cleanup)

			override, err := run.CodexConfig(mcpDisabled)
			Expect(err).NotTo(HaveOccurred())
			Expect(override).To(HavePrefix(prefix))
			Expect(args).To(ContainElements("-c", override))
			Expect(strings.Join(args, " ")).NotTo(ContainSubstring(bearer(run)), "the credential stays out of argv")
		},
		Entry("merged into the configured servers", false, "mcp_servers.captain="),
		Entry("replacing every server under mcp.disabled", true, "mcp_servers={captain="),
	)

	It("codex-cli empties mcp_servers under mcp.disabled without caller tools", func() {
		args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, ai.Request{Permissions: api.Permissions{MCP: api.MCP{Disabled: true}}})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(cleanup)
		Expect(args).To(ContainElements("-c", "mcp_servers={}"))
	})

	It("hands codex the caller-tool credential through the process environment", func(ctx SpecContext) {
		run := startCallerTools(ctx)
		req := ai.Request{}
		req.SetCwd(GinkgoT().TempDir())
		req.Setup = setupWithEnv(req.Setup, run.CodexEnv())
		cmd, closeSandbox, err := newCLICommand(ctx, "true", nil, &req, nil)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(closeSandbox)
		Expect(cmd.Env).To(ContainElement(run.CodexEnv()[0]))
	})
})
