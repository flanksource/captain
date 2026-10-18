package cmux

import (
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("cmux caller tools", func() {
	const toolName = "invoice_get"

	startCallerTools := func(ctx context.Context) *callertools.CLIRun {
		run, err := callertools.StartCLI(ctx, ai.Config{Tools: []api.ToolDefinition{{
			Name: toolName, DefaultPermission: api.ToolPolicyAsk,
			Handler: func(context.Context, map[string]any) (any, error) { return "ok", nil },
		}}}, ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(run.Close)
		return run
	}
	bearer := func(run *callertools.CLIRun) string {
		_, value, _ := strings.Cut(run.CodexEnv()[0], "=")
		return value
	}
	command := func(agent string, extra any, launch callerToolLaunch) string {
		return launch.launchCommand(AgentCommand(AgentCommandOpts{
			Agent: agent, AllowedTools: launch.allowedTools, ConfigOverrides: launch.configOverrides, Extra: extra,
		}))
	}

	It("launches claude with the caller-tool MCP config file and the ask tool pre-approved", func(ctx SpecContext) {
		run := startCallerTools(ctx)
		extra := &api.ClaudeCmuxOptions{}
		launch, err := prepareCallerTools(extra, ai.Request{Permissions: api.Permissions{MCP: api.MCP{Disabled: true}}}, run)
		Expect(err).NotTo(HaveOccurred())
		Expect(extra.MCPConfig).To(HaveLen(1))
		Expect(extra.MCPConfig[0]).To(BeAnExistingFile())

		got := command("claude", extra, launch)
		Expect(got).To(ContainSubstring("--allowedTools mcp__captain__" + toolName))
		Expect(got).To(ContainSubstring("--mcp-config " + extra.MCPConfig[0] + " --strict-mcp-config"))
		Expect(got).NotTo(ContainSubstring(bearer(run)), "the pasted command never shows the credential")
	})

	It("launches codex with the caller-tool server and a credential read from a 0600 file", func(ctx SpecContext) {
		run := startCallerTools(ctx)
		launch, err := prepareCallerTools(&api.CodexCmuxOptions{}, ai.Request{}, run)
		Expect(err).NotTo(HaveOccurred())
		override, err := run.CodexConfig(false)
		Expect(err).NotTo(HaveOccurred())

		got := command("codex", &api.CodexCmuxOptions{}, launch)
		Expect(got).To(HavePrefix(`CAPTAIN_CALLER_TOOLS_AUTHORIZATION="$(cat '`))
		Expect(got).To(ContainSubstring("-c " + shellSingleQuote(override)))
		Expect(got).NotTo(ContainSubstring(bearer(run)), "the pasted command never shows the credential")
		match := regexp.MustCompile(`\$\(cat '([^']+)'\)`).FindStringSubmatch(got)
		Expect(match).To(HaveLen(2))
		path := match[1]
		info, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		Expect(os.ReadFile(path)).To(Equal([]byte(bearer(run))))
	})

	It("refuses explicit cliArgs.mcpConfig under mcp.disabled", func() {
		_, err := prepareCallerTools(&api.ClaudeCmuxOptions{MCPConfig: []string{"servers.json"}},
			ai.Request{Permissions: api.Permissions{MCP: api.MCP{Disabled: true}}}, nil)
		Expect(err).To(MatchError(ContainSubstring("conflicts with disabled MCP")))
	})
})
