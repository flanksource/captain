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
		backend, provider, envVar, path, authHeader, authValue string,
		response map[string]any,
	) {
		GinkgoT().Setenv(envVar, "")
		credentials.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), "vault"))
		DeferCleanup(func() { credentials.SetPathForTesting("") })

		vault, err := credentials.DefaultVault()
		Expect(err).NotTo(HaveOccurred())
		Expect(vault.Set(provider, "vault-token")).To(Succeed())

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

		got, err := RunAIModels(AIModelsOptions{Backend: backend, Limit: 10})

		Expect(err).NotTo(HaveOccurred())
		Expect(encodeErr).NotTo(HaveOccurred())
		Expect(authorization).To(Equal(authValue))
		Expect(got).To(Equal(AIModelsResult{
			Total: 1,
			Rows:  []AIModelRow{{Model: "model-vault-test", Backend: backend, Input: "-", Output: "-", Context: "-", MaxTokens: "-"}},
		}))
	},
		Entry("for OpenAI", "openai", "openai", "OPENAI_API_KEY", "/v1/models", "Authorization", "Bearer vault-token",
			map[string]any{"data": []map[string]any{{"id": "model-vault-test"}}}),
		Entry("for Gemini", "gemini", "gemini", "GEMINI_API_KEY", "/v1beta/models", "x-goog-api-key", "vault-token",
			map[string]any{"models": []map[string]any{{"name": "models/model-vault-test"}}}),
		Entry("for DeepSeek", "deepseek", "deepseek", "DEEPSEEK_API_KEY", "/models", "Authorization", "Bearer vault-token",
			map[string]any{"data": []map[string]any{{"id": "model-vault-test"}}}),
	)
})
