package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Jev provider registration", func() {
	It("resolves jev-latest to the Jev API adapter", func() {
		const answer = `{"model":"jev-latest","answers":{"value":{"type":"choice","choice":"approved","confidence":0.9,"probabilities":{"approved":0.9,"rejected":0.1}}},"usage":{"input_tokens":20,"output_tokens":1}}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var sent map[string]any
			Expect(json.NewDecoder(r.Body).Decode(&sent)).To(Succeed())
			Expect(sent["state"]).To(Equal("Decision context"))
			Expect(sent["questions"]).To(HaveKey("value"))
			_, err := io.WriteString(w, answer)
			Expect(err).NotTo(HaveOccurred())
		}))
		DeferCleanup(server.Close)
		provider, err := ai.NewProvider(ai.Config{Model: api.Model{Name: "jev-latest"}, APIKey: "test-key", APIURL: server.URL})
		Expect(err).NotTo(HaveOccurred())
		Expect(provider.GetRuntime()).To(Equal(ai.RuntimeOf(ai.Jev, ai.ModeAPI)))
		Expect(provider.GetModel()).To(Equal("jev-latest"))
		response, err := provider.Execute(context.Background(), ai.Request{Prompt: api.Prompt{User: `{"model":"jev-latest","state":"Decision context","questions":{"value":{"type":"choice","instructions":"Choose an outcome","criteria":{"approved":null,"rejected":null}}}}`}})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StructuredData).To(MatchJSON(answer))
	})
})
