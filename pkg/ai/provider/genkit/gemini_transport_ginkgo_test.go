package genkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Gemini transport", func() {
	It("sends tool results with a Gemini-supported user role", func() {
		var roles []string
		transport := http.DefaultTransport
		http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var payload struct {
				Contents []struct {
					Role string `json:"role"`
				} `json:"contents"`
			}
			Expect(json.NewDecoder(request.Body).Decode(&payload)).To(Succeed())
			for _, content := range payload.Contents {
				roles = append(roles, content.Role)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"candidates":[{"content":{"parts":[{"text":"done"}],"role":"model"},"finishReason":"STOP"}]}`,
				)),
				Request: request,
			}, nil
		})
		DeferCleanup(func() {
			http.DefaultTransport = transport
		})

		model := api.Model{Name: "gemini-2.5-flash", Backend: api.BackendGemini}
		provider, err := New(ai.Config{Model: model, APIKey: "gemini-role-contract-test"})
		Expect(err).NotTo(HaveOccurred())

		response, err := provider.Execute(context.Background(), ai.Request{
			Model: model,
			Messages: []api.Message{
				{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Inspect the template."}}},
				{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
					ToolCallID: "call-1",
					Name:       "template_render",
					Input:      json.RawMessage(`{"template":"trial-balance-xero"}`),
				}}}},
				{Role: api.RoleTool, Parts: []api.Part{{Type: api.PartToolResult, ToolResult: &api.ToolResult{
					ToolCallID: "call-1",
					Output:     json.RawMessage(`{"issues":[]}`),
				}}}},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Text).To(Equal("done"))
		Expect(roles).To(Equal([]string{"user", "model", "user"}))
	})
})

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
