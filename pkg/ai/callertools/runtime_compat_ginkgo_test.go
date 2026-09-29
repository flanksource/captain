package callertools_test

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Deprecated caller-tool CanUseTool", func() {
	askTool := []api.ToolDefinition{{
		Name: "invoice_update", DefaultPermission: api.ToolPolicyAsk,
		Handler: func(context.Context, map[string]any) (any, error) { return "ok", nil },
	}}
	approve := func(context.Context, api.PermissionRequest) (api.PermissionDecision, error) {
		return api.PermissionDecision{Allow: true}, nil
	}

	It("refuses options that set both callbacks", func(ctx SpecContext) {
		_, err := callertools.New(callertools.Options{Context: ctx, Definitions: askTool, OnApproval: approve, CanUseTool: approve})
		Expect(err).To(MatchError(ContainSubstring("both OnApproval and the deprecated CanUseTool")))
	})

	It("still receives caller-tool approvals, which the old contract delivered", func(ctx SpecContext) {
		seen := make(chan api.PermissionRequest, 1)
		runtime, err := callertools.New(callertools.Options{
			Context: ctx, Definitions: askTool,
			CanUseTool: func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
				seen <- request
				return api.PermissionDecision{Allow: true}, nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(runtime.Close)
		client := authenticatedClient(ctx, runtime.Endpoint())
		DeferCleanup(client.Close)
		request := mcp.CallToolRequest{}
		request.Params.Name = "invoice_update"

		result, err := client.CallTool(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeFalse())
		Expect(<-seen).To(And(
			HaveField("Tool", "invoice_update"),
			HaveField("Kind", api.ApprovalKindTool),
			HaveField("LegacyContract", true),
		))
	}, SpecTimeout(10*time.Second))
})
