package aichat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Runtime profile HTTP resolution", func() {
	It("serves Captain's resolved preset and profile layers", func() {
		service := aichat.NewService(aichat.ServiceOptions{})
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(
			http.MethodPost,
			"/api/chat/runtime-profiles/resolve",
			api.RuntimeProfileResolveRequest{
				Profile: api.RuntimeProfile{
					ID: "review", Name: "Review", Presets: []string{"defaults"},
					Spec: api.Spec{Prompt: api.Prompt{User: "Review the diff."}},
				},
				Presets: []api.RuntimePreset{{
					ID: "defaults", Name: "Defaults", Scope: api.SpecLayerGlobal,
					Spec: api.RuntimePresetSpec{Model: api.Model{
						Name: "gpt-5", Mode: api.ModeAgent,
					}},
				}},
			},
		))

		Expect(response.Code).To(Equal(http.StatusOK))
		var payload api.RuntimeProfileResolveResponse
		Expect(json.Unmarshal(response.Body.Bytes(), &payload)).To(Succeed())
		Expect(payload.Resolved.Spec.Model.Name).To(Equal("gpt-5"))
		Expect(payload.Resolved.Spec.Prompt.User).To(Equal("Review the diff."))
		Expect(payload.Resolved.Trace).To(HaveLen(2))
	})

	It("rejects task-owned fields inside a preset", func() {
		service := aichat.NewService(aichat.ServiceOptions{})
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/chat/runtime-profiles/resolve",
			strings.NewReader(`{
				"profile":{"id":"review","name":"Review","spec":{}},
				"presets":[{"id":"bad","name":"Bad","scope":"global","spec":{"prompt":{"user":"task"}}}]
			}`),
		)
		request.Header.Set("Content-Type", "application/json")
		service.Handler().ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring(`unknown field "prompt"`))
	})

	It("returns Captain's effective caller-tool permissions", func() {
		readOnly, nonDestructive, destructive := true, false, true
		service := aichat.NewService(aichat.ServiceOptions{
			Tools: aichat.StaticToolProvider([]api.ToolDefinition{
				{
					Name: "invoice_list", Group: "billing.read", DefaultPermission: api.ToolPolicyAuto,
					ReadOnlyHint: &readOnly, DestructiveHint: &nonDestructive,
					Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
				},
				{
					Name: "invoice_update", Group: "billing.write", DefaultPermission: api.ToolPolicyAuto,
					DestructiveHint: &destructive, Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
				},
			}),
			ToolPolicy: api.PermissionPolicy{{
				ToolMatch: api.ToolMatch{Group: api.MatchPatterns{"billing.write"}},
				Policy:    api.ToolPolicyDeny,
			}},
		})
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(
			http.MethodPost,
			"/api/chat/runtime-profiles/resolve",
			api.RuntimeProfileResolveRequest{Profile: api.RuntimeProfile{
				ID: "review", Name: "Review", Spec: api.Spec{Model: api.Model{
					Name: "gpt-5", Mode: api.ModeAgent,
				}},
			}},
		))

		Expect(response.Code).To(Equal(http.StatusOK))
		var payload api.RuntimeProfileResolveResponse
		Expect(json.Unmarshal(response.Body.Bytes(), &payload)).To(Succeed())
		Expect(payload.Tools).To(HaveLen(2))
		Expect(payload.Permissions).To(Equal(map[string]api.ToolPolicy{
			"invoice_list":   api.ToolPolicyAllow,
			"invoice_update": api.ToolPolicyDeny,
		}))
		Expect(payload.PermissionSupport["invoice_update"].Kind).To(Equal(api.SupportNative))
		Expect(payload.EffectivePolicy).To(HaveLen(1))
	})
})
