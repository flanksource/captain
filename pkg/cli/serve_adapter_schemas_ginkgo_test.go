package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

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
		embedded, err := adapters.Schemas()
		Expect(err).NotTo(HaveOccurred())
		Expect(documents).To(HaveLen(len(embedded)))
		Expect(documents[0].Provider).To(Equal("anthropic"))
		Expect(documents[0].Mode).To(Equal("api"))
		Expect(documents[0].Schema).NotTo(BeEmpty())
	})

	DescribeTable("validates a value against an embedded or carried schema",
		func(body string, status int, valid bool) {
			mux := http.NewServeMux()
			registerAdapterSchemaHandlers(mux)
			response := httptest.NewRecorder()

			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/captain/ai/adapter-schemas/validate", strings.NewReader(body)))

			Expect(response.Code).To(Equal(status), response.Body.String())
			if status == http.StatusOK {
				var result adapters.ValidationResult
				Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
				Expect(result.Valid).To(Equal(valid))
			}
		},
		Entry("embedded Captain resource", `{"id":"captain/permissions","value":{"tools":{"Bash":"unknown"}}}`, http.StatusOK, false),
		Entry("published native resource carrying its schema", `{"id":"anthropic/sandbox","schema":{"type":"object","properties":{"enabled":{"type":"boolean"}}},"value":{"enabled":true}}`, http.StatusOK, true),
		Entry("published native resource without its schema", `{"id":"anthropic/sandbox","value":{"enabled":true}}`, http.StatusNotFound, false),
		Entry("carried schema that is not an object", `{"id":"anthropic/sandbox","schema":[],"value":{}}`, http.StatusBadRequest, false),
	)

	It("publishes the viewer catalog in OpenAPI", func() {
		spec := &rpc.OpenAPISpec{}

		addCaptainAdapterSchemaPaths(spec)

		Expect(spec.Paths).To(HaveKey("/api/captain/ai/adapter-schemas"))
		operation := spec.Paths["/api/captain/ai/adapter-schemas"]["get"]
		Expect(operation.OperationID).To(Equal("listAdapterSchemas"))
		Expect(spec.Paths).To(HaveKey("/api/captain/ai/adapter-schemas/validate"))
		validation := spec.Paths["/api/captain/ai/adapter-schemas/validate"]["post"]
		Expect(validation.RequestBody.Content["application/json"].Schema.Required).To(ConsistOf("id", "value"))
		Expect(validation.Responses).To(HaveKey("404"))
	})
})
