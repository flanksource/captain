package openaimock

import (
	"encoding/json"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Responses namespace tools", func() {
	It("emits and normalizes the namespace separately from the function name", func() {
		response := Respond{FunctionCall: &FunctionCall{
			Namespace: "mcp__captain", Name: "accounts_edit", CallID: "call_account",
			Arguments: map[string]any{"id": "acc-1"},
		}}
		item := response.items("resp_mock_namespace_1")[0].done()
		Expect(item).To(HaveKeyWithValue("namespace", "mcp__captain"))
		Expect(item).To(HaveKeyWithValue("name", "accounts_edit"))

		body, err := json.Marshal(map[string]any{
			"model": "gpt-5", "input": []any{
				item,
				map[string]any{"type": "function_call_output", "call_id": "call_account", "output": "updated"},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		request := httptest.NewRequest("POST", "/v1/responses", nil)
		_, normalized, err := decodeResponses(request, body)
		Expect(err).NotTo(HaveOccurred())
		Expect(normalized.ToolResultNames()).To(Equal([]string{"mcp__captain__accounts_edit"}))
	})

	It("assigns distinct response and item identities to successive requests", func() {
		server := &Server{}
		first := server.nextWireID("resp", "gpt-5")
		second := server.nextWireID("resp", "gpt-5")
		response := Respond{Text: "done"}

		Expect(second).NotTo(Equal(first))
		Expect(response.items(second)[0].ID).NotTo(Equal(response.items(first)[0].ID))
	})
})
