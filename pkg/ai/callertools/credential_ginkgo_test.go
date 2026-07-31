package callertools_test

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/mark3labs/mcp-go/mcp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Caller-tool credential lease", func() {
	It("exposes only the credential hash and revalidates the persisted lease", func(ctx SpecContext) {
		var revoked atomic.Bool
		runtime, err := callertools.New(callertools.Options{
			Definitions: []api.ToolDefinition{{
				Name: "account_edit", DefaultPermission: api.ToolModeOn,
				Handler: func(context.Context, map[string]any) (any, error) {
					return map[string]any{"updated": true}, nil
				},
			}},
			SessionID: "captain-session-1",
			ValidateCredential: func(context.Context) error {
				if revoked.Load() {
					return errors.New("caller-tool credential is revoked")
				}
				return nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(runtime.Close)

		hash := runtime.CredentialHash()
		Expect(hash).To(HaveLen(32))
		Expect(string(hash)).NotTo(ContainSubstring("cap_captain-session-1"))

		client := authenticatedClient(ctx, runtime.Endpoint())
		DeferCleanup(client.Close)
		request := mcp.CallToolRequest{}
		request.Params.Name = "account_edit"
		request.Params.Arguments = map[string]any{"id": "acc-1"}
		result, err := client.CallTool(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeFalse())

		revoked.Store(true)
		_, err = client.ListTools(ctx, mcp.ListToolsRequest{})
		Expect(err).To(HaveOccurred())
	})
})
