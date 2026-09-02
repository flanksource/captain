package aichat_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Chat tool catalog HTTP", func() {
	It("serves frontend metadata for every configured tool source", func() {
		service := aichat.NewService(aichat.ServiceOptions{
			Profile: aichat.RuntimeProfileProviderFunc(func(context.Context, ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
				return aichat.RuntimeProfile{}, errors.New("runtime profile unavailable")
			}),
			Tools: aichat.StaticToolProvider([]api.ToolDefinition{{
				Name: "invoice_get", Group: "billing", Description: "Get invoice",
				InputSchema: map[string]any{"type": "object"},
				Handler:     func(context.Context, map[string]any) (any, error) { return nil, nil },
			}}),
			MCP: aichat.StaticToolProvider([]api.ToolDefinition{{
				Name: "docs_search", Group: "docs", Description: "Search documentation",
				Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
			}}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/tools", nil))

		Expect(response.Code).To(Equal(http.StatusOK))
		var catalog aichat.ToolCatalogResponse
		Expect(json.Unmarshal(response.Body.Bytes(), &catalog)).To(Succeed())
		Expect(catalog.Tools).To(ConsistOf(
			HaveField("Name", "invoice_get"),
			HaveField("Name", "docs_search"),
		))
	})
})
