package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/captain/pkg/adapters"
	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("adapter schema viewer API", func() {
	It("serves every embedded runtime schema", func() {
		mux := http.NewServeMux()
		registerAdapterSchemaHandlers(mux)
		response := httptest.NewRecorder()

		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/captain/ai/adapter-schemas", nil))

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(response.Header().Get("Content-Type")).To(Equal("application/json"))
		var documents []adapters.SchemaDocument
		Expect(json.Unmarshal(response.Body.Bytes(), &documents)).To(Succeed())
		Expect(documents).To(HaveLen(9))
		Expect(documents[0].Provider).To(Equal("anthropic"))
		Expect(documents[0].Mode).To(Equal("api"))
		Expect(documents[0].Schema).NotTo(BeEmpty())
	})

	It("publishes the viewer catalog in OpenAPI", func() {
		spec := &rpc.OpenAPISpec{}

		addCaptainAdapterSchemaPaths(spec)

		Expect(spec.Paths).To(HaveKey("/api/captain/ai/adapter-schemas"))
		operation := spec.Paths["/api/captain/ai/adapter-schemas"]["get"]
		Expect(operation.OperationID).To(Equal("listAdapterSchemas"))
	})
})
