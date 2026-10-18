package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"

	"github.com/flanksource/captain/pkg/credentials"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ai models credential resolution", Serial, func() {
	DescribeTable("uses the Captain vault when the provider environment variable is unset", func(
		provider, credentialKey, envVar, path, authHeader, authValue string,
		response map[string]any,
	) {
		GinkgoT().Setenv(envVar, "")
		GinkgoT().Setenv("TYPESAFE_BASE_URL", "")
		credentials.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), "vault"))
		DeferCleanup(func() { credentials.SetPathForTesting("") })

		vault, err := credentials.DefaultVault()
		Expect(err).NotTo(HaveOccurred())
		Expect(vault.Set(credentialKey, "vault-token")).To(Succeed())

		authorization := ""
		var encodeErr error
		mux := http.NewServeMux()
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			authorization = r.Header.Get(authHeader)
			encodeErr = json.NewEncoder(w).Encode(response)
		})
		server := httptest.NewServer(mux)
		DeferCleanup(server.Close)

		originalTransport := http.DefaultClient.Transport
		http.DefaultClient.Transport = aiModelsRewriteTransport{base: server.URL, inner: server.Client().Transport}
		DeferCleanup(func() { http.DefaultClient.Transport = originalTransport })

		got, err := RunAIModels(AIModelsOptions{Provider: provider, Limit: 10})

		Expect(err).NotTo(HaveOccurred())
		Expect(encodeErr).NotTo(HaveOccurred())
		Expect(authorization).To(Equal(authValue))
		Expect(got).To(Equal(AIModelsResult{
			Total: 1,
			Rows:  []AIModelRow{{Model: "model-vault-test", Provider: provider, Mode: "api", Input: "-", Output: "-", Context: "-", MaxTokens: "-"}},
		}))
	},
		Entry("for OpenAI", "openai", "openai", "OPENAI_API_KEY", "/v1/models", "Authorization", "Bearer vault-token",
			map[string]any{"data": []map[string]any{{"id": "model-vault-test"}}}),
		Entry("for Gemini", "google", "google", "GEMINI_API_KEY", "/v1beta/models", "x-goog-api-key", "vault-token",
			map[string]any{"models": []map[string]any{{"name": "models/model-vault-test"}}}),
		Entry("for DeepSeek", "deepseek", "deepseek", "DEEPSEEK_API_KEY", "/models", "Authorization", "Bearer vault-token",
			map[string]any{"data": []map[string]any{{"id": "model-vault-test"}}}),
		Entry("for Jev", "jev", "jev", "TYPESAFE_API_KEY", "/v1/models", "Authorization", "Bearer vault-token",
			map[string]any{"models": []map[string]any{{"name": "model-vault-test", "description": "Decision model", "release_date": "2026-09-15"}}}),
	)

	It("shows the catalog's free Jev output rate as free", func() {
		GinkgoT().Setenv("TYPESAFE_API_KEY", "test-key")
		GinkgoT().Setenv("TYPESAFE_BASE_URL", "")
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/v1/models"))
			Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-key"))
			Expect(json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"name": "jev-latest", "description": "Decision model", "release_date": "2026-09-15"}},
			})).To(Succeed())
		})
		server := httptest.NewServer(mux)
		DeferCleanup(server.Close)
		originalTransport := http.DefaultClient.Transport
		http.DefaultClient.Transport = aiModelsRewriteTransport{base: server.URL, inner: server.Client().Transport}
		DeferCleanup(func() { http.DefaultClient.Transport = originalTransport })

		result, err := RunAIModels(AIModelsOptions{Provider: "jev", Limit: 10})
		Expect(err).NotTo(HaveOccurred())
		rows := result.(AIModelsResult).Rows
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Input).NotTo(Equal("-"))
		Expect(rows[0].Output).To(Equal("Free"))
	})

	It("keeps a zero rate without free pricing semantics unknown", func() {
		Expect(formatPrice(0)).To(Equal("-"))
	})
})
