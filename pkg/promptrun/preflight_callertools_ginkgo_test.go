package promptrun_test

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	clickyentity "github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The caller tools a host hands promptrun reach the agent over captain's MCP
// endpoint, and a POST operation resolves to ask through the HTTP-verb strategy.
// Each check here is a way such a run would otherwise fail only after the model
// had been paid for.
var _ = Describe("promptrun.Preflight caller tools", func() {
	const toolName = "sql_execute"
	noop := func(context.Context, map[string]any) (any, error) { return "ok", nil }
	operationTool := func(name, method string) api.ToolDefinition {
		return api.ToolDefinition{
			Name: name, Handler: noop,
			Operation: &clickyentity.RPCOperation{Name: name, Method: method, Path: "/api/v1/" + name},
		}
	}
	broker := func(context.Context, api.ApprovalRequest) (api.ApprovalDecision, error) {
		return api.ApprovalDecision{Allow: true}, nil
	}

	var in promptrun.Input
	BeforeEach(func() {
		in = promptrun.Input{
			Resolved: api.ResolvedSpec{Spec: api.Spec{Model: api.Model{Name: "sonnet", Mode: api.ModeAgent}, Prompt: api.Prompt{User: "fix the fixture"}}},
			Config:   ai.Config{Tools: []api.ToolDefinition{operationTool(toolName, http.MethodPost)}},
			Timeout:  time.Minute,
		}
	})

	expectRefused := func(message string) {
		_, err := promptrun.Preflight(in)
		Expect(err).To(MatchError(ContainSubstring(message)))
		_, runErr := promptrun.Run(context.Background(), in)
		Expect(runErr).To(MatchError(err.Error()))
	}

	It("refuses a POST operation tool that resolves to ask with no broker, naming the tool and runtime", func() {
		_, err := promptrun.Preflight(in)
		Expect(err).To(MatchError(
			"promptrun: caller tools " + toolName + " resolve to ask on \"anthropic agent\" but no approval broker is attached; " +
				"allow them with a permission set (--perms) or attach Config.OnApproval (e.g. a terminal broker)"))
		expectRefused(toolName)
	})

	It("names only the asking tools, sorted, and caps the list", func() {
		in.Config.Tools = []api.ToolDefinition{operationTool("segment_get", http.MethodGet)}
		for i := 12; i > 0; i-- {
			in.Config.Tools = append(in.Config.Tools, operationTool(fmt.Sprintf("write_%02d", i), http.MethodPost))
		}
		_, err := promptrun.Preflight(in)
		Expect(err).To(MatchError(ContainSubstring(
			"caller tools write_01, write_02, write_03, write_04, write_05, write_06, write_07, write_08, write_09, write_10 and 2 more resolve to ask")))
		Expect(err.Error()).NotTo(ContainSubstring("segment_get"))
	})

	It("admits the tool once a spec rule allows it", func() {
		in.Resolved.Spec.ToolPolicy = api.PermissionPolicy{{ToolMatch: api.ToolMatch{Name: api.MatchPatterns{toolName}}, Policy: api.ToolPolicyAllow}}
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
	})

	It("admits the tool once a spec preference allows it", func() {
		in.Resolved.Spec.ToolPreferences = api.ToolPreferences{toolName: api.ToolPolicyAllow}
		_, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits the tool when Config.OnApproval is attached", func() {
		in.Config.OnApproval = broker
		_, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits the tool when Captain's durable approval broker is requested", func() {
		in.Approvals = &promptrun.ApprovalOptions{RequestedBy: "dashboard"}
		in.OnEvent = func(int, ai.Event) {}
		_, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
	})

	It("does not resolve tools for a supplied provider, which owns its own tool wiring", func() {
		in.Provider = &scriptedProvider{model: "claude-sonnet-5"}
		_, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses a caller tool the strategies cannot resolve, even with a broker attached", func() {
		in.Config.Tools = []api.ToolDefinition{{Name: toolName}}
		expectRefused("has no handler")
		in.Config.OnApproval = broker
		expectRefused("has no handler")
	})

	DescribeTable("admits caller tools when MCP is disabled, since captain's caller-tool server is exempt",
		func(provider string) {
			in.Config.OnApproval = broker
			in.Resolved.Spec.Model = api.Model{Name: provider, Mode: api.ModeAgent}
			in.Resolved.Spec.Permissions.MCP.Disabled = true
			_, err := promptrun.Preflight(in)
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("anthropic agent", "sonnet"),
		Entry("openai agent", "gpt-5.4"),
	)

	DescribeTable("admits caller tools on the cli and cmux runtimes, which serve them over a run-scoped MCP server",
		func(model string, mode api.RuntimeMode) {
			in.Config.OnApproval = broker
			in.Resolved.Spec.Model = api.Model{Name: model, Mode: mode}
			_, err := promptrun.Preflight(in)
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("anthropic cli", "sonnet", api.ModeCLI),
		Entry("anthropic cmux", "sonnet", api.ModeCmux),
		Entry("openai cli", "gpt-5.4", api.ModeCLI),
		Entry("openai cmux", "gpt-5.4", api.ModeCmux),
	)

	It("refuses caller tools on a runtime that cannot expose them, naming it", func() {
		in.Config.OnApproval = broker
		in.Resolved.Spec.Model = api.Model{Name: "gemini-2.5-pro", Mode: api.ModeCLI}
		expectRefused(`runtime "google cli" cannot expose caller tools`)
	})

	It("checks every fallback candidate, not only the primary model", func() {
		in.Config.OnApproval = broker
		in.Resolved.Spec.Fallbacks = []api.Model{{Name: "gemini-2.5-pro", Mode: api.ModeCLI}}
		expectRefused(`runtime "google cli" cannot expose caller tools`)
	})

	Context("declared inline in the spec", func() {
		BeforeEach(func() { in.Config.Tools = nil })

		It("admits a destructive inline tool like a host tool: refused without a broker", func() {
			in.Resolved.Spec.Tools = api.InlineTools{"drop_table": {Exec: "true", Destructive: true}}
			expectRefused("caller tools drop_table resolve to ask")
		})

		It("admits a readonly inline tool without a broker", func() {
			in.Resolved.Spec.Tools = api.InlineTools{"lookup": {Exec: "true", ReadOnly: true}}
			_, err := promptrun.Preflight(in)
			Expect(err).NotTo(HaveOccurred())
		})

		It("refuses inline tools under a sandbox that cannot run them", func() {
			in.Resolved.Spec.Tools = api.InlineTools{"lookup": {Exec: "true", ReadOnly: true}}
			in.Resolved.Spec.Model = api.Model{Name: "sonnet", Mode: api.ModeCLI}
			in.Config.SandboxSelection = &api.SandboxConfig{Kind: api.SandboxDocker}
			expectRefused(`tools.lookup: sandbox "docker" cannot run inline tools`)
		})

		It("refuses placement: sandbox on a run with no sandbox to place it in", func() {
			in.Resolved.Spec.Tools = api.InlineTools{"lookup": {Exec: "true", ReadOnly: true, Placement: api.ToolPlacementSandbox}}
			expectRefused(`tools.lookup: sandbox "off" cannot run an inline tool with placement "sandbox"; it supports: host`)
		})

		DescribeTable("refuses a sidecar tool the sidecar could not run as declared",
			func(tool api.InlineTool, message string) {
				tool.Exec, tool.Placement = "true", api.ToolPlacementSidecar
				in.Resolved.Spec.Tools = api.InlineTools{"lookup": tool}
				in.Resolved.Spec.Model = api.Model{Name: "sonnet", Mode: api.ModeCLI}
				in.Config.SandboxSelection = &api.SandboxConfig{Kind: api.SandboxGitAgent}
				expectRefused(message)
			},
			Entry("the supervisor's real secrets", api.InlineTool{ReadOnly: true, Credentials: api.ToolCredentialsDirect}, "would send the supervisor's secrets to the sidecar"),
			Entry("a call nobody could approve", api.InlineTool{Destructive: true}, "tools.lookup needs approval, but a sidecar tool runs where no one can approve it"),
			Entry("secrets with no mode", api.InlineTool{ReadOnly: true, Setup: shell.Setup{Connections: connection.ExecConnections{AWS: &connection.AWSConnection{}}}}, "needs an explicit credentials"),
		)

		It("admits a readonly sidecar tool and keeps it off the supervisor's caller tools", func() {
			in.Resolved.Spec.Tools = api.InlineTools{"lookup": {Exec: "true", ReadOnly: true, Placement: api.ToolPlacementSidecar}}
			in.Resolved.Spec.Model = api.Model{Name: "sonnet", Mode: api.ModeCLI}
			in.Config.SandboxSelection = &api.SandboxConfig{Kind: api.SandboxGitAgent}
			_, err := promptrun.Preflight(in)
			Expect(err).NotTo(HaveOccurred())
		})

		It("refuses inline tools on a runtime that cannot expose them", func() {
			in.Resolved.Spec.Tools = api.InlineTools{"lookup": {Exec: "true", ReadOnly: true}}
			in.Resolved.Spec.Model = api.Model{Name: "gemini-2.5-pro", Mode: api.ModeCLI}
			expectRefused(`runtime "google cli" cannot expose caller tools`)
		})
	})

	It("fires none of the caller-tool checks for a run with no caller tools", func() {
		in.Config.Tools = nil
		in.Resolved.Spec.Permissions.MCP.Disabled = true
		in.Resolved.Spec.Mode = api.ModeCLI
		warnings, err := promptrun.Preflight(in)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
	})
})
