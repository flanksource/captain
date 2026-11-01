package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("adapter resource validation", func() {
	DescribeTable("validates values without applying configuration", func(body string, status int, expected string) {
		mux := http.NewServeMux()
		registerAdapterSchemaHandlers(mux)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
			"/api/captain/ai/adapter-schemas/validate", strings.NewReader(body)))
		Expect(response.Code).To(Equal(status), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring(expected))
	},
		Entry("valid permissions", `{"id":"captain/permissions","value":{"mode":"plan"}}`, http.StatusOK, `"valid":true`),
		Entry("invalid permissions", `{"id":"captain/permissions","value":{"mode":"unknown"}}`, http.StatusOK, `"valid":false`),
		Entry("unknown resource", `{"id":"missing/config","value":{}}`, http.StatusNotFound, "unknown schema"),
		Entry("missing value", `{"id":"captain/permissions"}`, http.StatusBadRequest, "value is required"),
		Entry("missing identity", `{"value":{}}`, http.StatusBadRequest, "id is required"),
		Entry("null value", `{"id":"captain/permissions","value":null}`, http.StatusOK, `"valid":false`),
		Entry("unknown request field", `{"id":"captain/permissions","value":{},"apply":true}`, http.StatusBadRequest, "unknown field"),
		Entry("trailing document", `{"id":"captain/permissions","value":{}} {}`, http.StatusBadRequest, "single JSON"),
	)
})
