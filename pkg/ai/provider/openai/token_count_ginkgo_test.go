package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTokenCounting(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OpenAI Token Counting Suite")
}

var _ = Describe("Token counting", func() {
	It("uses Responses input_tokens with native input and no generation controls", func() {
		var body map[string]json.RawMessage
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/responses/input_tokens"))
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"input_tokens":17}`))
			Expect(err).NotTo(HaveOccurred())
		}))
		defer server.Close()
		p, err := New(api.Config{Model: api.Model{Name: "gpt-5", Mode: api.ModeAPI, Provider: api.OpenAI}, APIKey: "example-key", APIURL: server.URL})
		Expect(err).NotTo(HaveOccurred())
		count, err := p.CountTokens(context.Background(), api.Spec{
			Prompt:      api.Prompt{User: "full content", System: "instructions", AppendSystem: "extra instructions"},
			Permissions: api.Permissions{Tools: api.Tools{"lookup": api.ToolPolicyDeny}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(count.Tokens).To(Equal(17))
		Expect(body).To(HaveKey("input"))
		Expect(body).To(HaveKeyWithValue("instructions", json.RawMessage(`"instructions\nextra instructions"`)))
		Expect(body).NotTo(HaveKey("store"))
		Expect(body).NotTo(HaveKey("max_output_tokens"))
		Expect(body).NotTo(HaveKey("stream"))
	})
	It("counts available text and discloses unavailable media rather than inventing image tokens", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]json.RawMessage
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			Expect(string(body["input"])).To(ContainSubstring("available text"))
			Expect(string(body["input"])).NotTo(ContainSubstring("input_image"))
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"input_tokens":9}`))
			Expect(err).NotTo(HaveOccurred())
		}))
		defer server.Close()
		p, err := New(api.Config{Model: api.Model{Name: "gpt-5", Mode: api.ModeAPI, Provider: api.OpenAI}, APIKey: "example-key", APIURL: server.URL})
		Expect(err).NotTo(HaveOccurred())
		count, err := p.CountTokens(context.Background(), api.Spec{Prompt: api.Prompt{User: "available text", Attachments: []api.AttachmentRef{{Path: "unavailable.png", MediaType: "image/png"}}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(api.TokenCount{Tokens: 9, Excluded: []string{"unavailable attachment content"}}))
	})
})
