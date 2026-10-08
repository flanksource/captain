package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Session token sizing HTTP contract", func() {
	It("rejects invalid methods and unknown fields before accessing a database", func() {
		for _, body := range []string{`{"method":"generate"}`, `{"method":"estimate","content":"untrusted"}`, `{"method":"estimate"}{}`, `{`} {
			response := httptest.NewRecorder()
			SessionHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/example/tokens", strings.NewReader(body)))
			Expect(response.Code).To(Equal(http.StatusBadRequest))
			Expect(response.Header().Get("Content-Type")).To(HavePrefix("application/json"))
		}
	})
	It("publishes the request and row footprint response schemas", func() {
		spec := &rpc.OpenAPISpec{}
		addCaptainSessionTokenPaths(spec)
		operation := spec.Paths["/api/captain/sessions/{id}/tokens"]["post"]
		Expect(operation.OperationID).To(Equal("sizeSessionTokens"))
		Expect(operation.RequestBody.Content["application/json"].Schema.Properties["method"].Enum).To(Equal([]any{"estimate", "provider"}))
		data, err := json.Marshal(operation)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"rowIds"`))
		Expect(string(data)).To(ContainSubstring(`"coverage"`))
		Expect(string(data)).To(ContainSubstring(`"costUSD"`))
	})
})
