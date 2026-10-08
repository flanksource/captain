package genkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Token counting", func() {
	It("sends Gemini a full count request including system tools and response schema", func() {
		var body map[string]json.RawMessage
		transport := http.DefaultTransport
		http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			Expect(r.URL.Path).To(Equal("/v1beta/models/gemini-2.5-pro:countTokens"))
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"totalTokens":23}`)), Request: r}, nil
		})
		DeferCleanup(func() { http.DefaultTransport = transport })
		p := &Provider{provider: ai.Google, cfg: api.Config{Model: api.Model{Name: "gemini-2.5-pro", Mode: api.ModeAPI}, APIKey: "example-key", Tools: []api.ToolDefinition{{Name: "read", Handler: func(context.Context, map[string]any) (any, error) { Fail("counting executed a tool"); return nil, nil }}}}}
		count, err := p.CountTokens(context.Background(), api.Spec{Prompt: api.Prompt{User: "hello", System: "instructions", SchemaJSON: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(count.Tokens).To(Equal(23))
		Expect(body).NotTo(HaveKey("contents"))
		var generation map[string]json.RawMessage
		Expect(json.Unmarshal(body["generateContentRequest"], &generation)).To(Succeed())
		Expect(generation).To(HaveKey("systemInstruction"))
		Expect(generation).To(HaveKey("tools"))
		Expect(generation).To(HaveKey("generationConfig"))
		Expect(generation).To(HaveKey("contents"))
	})
	It("sends Anthropic only count parameters and includes tools and schema", func() {
		var body map[string]json.RawMessage
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/v1/messages/count_tokens"))
			Expect(r.Header.Get("X-Api-Key")).To(Equal("example-key"))
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"input_tokens":21}`))
			Expect(err).NotTo(HaveOccurred())
		}))
		defer server.Close()
		p := &Provider{provider: ai.Anthropic, cfg: api.Config{Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAPI}, APIKey: "example-key", APIURL: server.URL,
			Tools: []api.ToolDefinition{{Name: "read", Description: "Read text", InputSchema: map[string]any{"type": "object"}, Handler: func(context.Context, map[string]any) (any, error) { Fail("counting executed a tool"); return nil, nil }}},
		}}
		count, err := p.CountTokens(context.Background(), api.Spec{Prompt: api.Prompt{User: "hello", System: "instructions", SchemaJSON: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(count.Tokens).To(Equal(21))
		Expect(body).To(HaveKey("system"))
		Expect(body).To(HaveKey("tools"))
		Expect(body).To(HaveKey("output_config"))
		Expect(body).NotTo(HaveKey("max_tokens"))
		Expect(body).NotTo(HaveKey("stream"))
	})

	It("surfaces missing count fields instead of reporting zero", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{}`))
			Expect(err).NotTo(HaveOccurred())
		}))
		defer server.Close()
		p := &Provider{provider: ai.Anthropic, cfg: api.Config{Model: api.Model{Name: "claude-sonnet-4-6"}, APIKey: "example-key", APIURL: server.URL}}
		_, err := p.CountTokens(context.Background(), api.Spec{Prompt: api.Prompt{User: "hello"}})
		Expect(err).To(MatchError(ContainSubstring("input_tokens")))
	})
})
