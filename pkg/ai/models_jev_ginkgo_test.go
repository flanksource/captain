package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Jev model listing", func() {
	DescribeTable("resolves the TypeSafe model endpoint and metadata", func(basePath string) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/tenant/v1/models"))
			Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-key"))
			Expect(json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"name": "jev-latest", "description": "General purpose decision model", "release_date": "2026-09-15"}},
			})).To(Succeed())
		}))
		DeferCleanup(server.Close)

		models, err := ListModelsWithAPIKeyAndURL(context.Background(), Jev, "test-key", server.URL+basePath)
		Expect(err).NotTo(HaveOccurred())
		Expect(models).To(Equal([]ModelDef{{
			ID: "jev-latest", Name: "jev-latest", Provider: "jev", Mode: ModeAPI,
			ReleaseDate: "2026-09-15", CapabilitiesKnown: true, Priority: 1,
		}}))
	},
		Entry("base URL", "/tenant"),
		Entry("versioned URL", "/tenant/v1"),
	)
})
