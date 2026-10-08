package tools_test

import (
	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ApprovalDenial", func() {
	destructive := true
	options := tools.ResolveOptions{
		Preferences: api.ToolPreferences{"Bash": api.ToolPolicyDeny},
		Policy: api.PermissionPolicy{
			{ToolMatch: api.ToolMatch{Parent: api.MatchPatterns{"playwright"}, Destructive: &destructive}, Policy: api.ToolPolicyDeny},
		},
	}

	DescribeTable("denies a request the resolved policy denies",
		func(request api.ApprovalRequest, want string) {
			reason, denied, err := tools.ApprovalDenial(options, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(denied).To(BeTrue())
			Expect(reason).To(ContainSubstring(want))
		},
		Entry("a tool denied by name", api.ApprovalRequest{Tool: "Bash", Kind: api.ApprovalKindCommand, Command: &api.CommandApproval{Command: "rm -rf /"}}, `"Bash"`),
		Entry("an MCP tool matched on its hints", api.ApprovalRequest{
			Tool: "mcp__playwright__browser_tabs", Kind: api.ApprovalKindTool,
			Info: &api.ToolInfo{Name: "mcp__playwright__browser_tabs", Parent: "playwright", DestructiveHint: &destructive},
		}, "browser_tabs"),
	)

	DescribeTable("leaves other requests to the callback",
		func(request api.ApprovalRequest) {
			_, denied, err := tools.ApprovalDenial(options, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(denied).To(BeFalse())
		},
		Entry("a tool no rule denies", api.ApprovalRequest{Tool: "Edit", Kind: api.ApprovalKindFilesystem, Filesystem: &api.FilesystemApproval{Operation: api.FilesystemEdit}}),
		Entry("a non-destructive tool from the same MCP server", api.ApprovalRequest{
			Tool: "mcp__playwright__browser_snapshot", Kind: api.ApprovalKindTool,
			Info: &api.ToolInfo{Name: "mcp__playwright__browser_snapshot", Parent: "playwright"},
		}),
		Entry("an elicitation, which asks for input rather than running a tool", api.ApprovalRequest{
			Tool: "Bash", Kind: api.ApprovalKindElicitation,
			Elicitation: &api.ElicitationApproval{Server: "github", Mode: api.ElicitationModeURL, URL: "https://example.com"},
		}),
	)

	It("reports an invalid policy instead of guessing", func() {
		_, _, err := tools.ApprovalDenial(tools.ResolveOptions{Preferences: api.ToolPreferences{"Bash": "sometimes"}}, api.ApprovalRequest{Tool: "Bash", Kind: api.ApprovalKindTool})
		Expect(err).To(HaveOccurred())
	})
})
