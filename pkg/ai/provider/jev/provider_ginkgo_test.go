package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func newConvertedProvider(cfg ai.Config) (ai.Provider, error) {
	provider, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return middleware.WithJevConversion()(provider)
}

var _ = Describe("Jev output", func() {
	const objectSchema = `{"type":"object","description":"Classify the ticket","properties":{"route":{"type":"string","description":"Select the responsible team","enum":["billing","technical"]},"urgency":{"type":"string","enum":["low","high"]}},"required":["route","urgency"],"additionalProperties":false}`

	It("sends enum properties as choice questions and returns structured output with usage", func() {
		var sent map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/v1/systemone"))
			Expect(r.Header.Get("Authorization")).To(Equal("Bearer test-key"))
			Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
			Expect(json.NewDecoder(r.Body).Decode(&sent)).To(Succeed())
			_, err := io.WriteString(w, `{"model":"jev-1.14.0","answers":{"route":{"type":"choice","choice":"billing","confidence":0.9,"probabilities":{"billing":0.9,"technical":0.1}},"urgency":{"type":"choice","choice":"high","confidence":0.8,"probabilities":{"low":0.2,"high":0.8}}},"usage":{"input_tokens":120,"output_tokens":2}}`)
			Expect(err).NotTo(HaveOccurred())
		}))
		DeferCleanup(server.Close)

		provider, err := newConvertedProvider(ai.Config{Model: api.Model{Name: "jev-latest"}, APIKey: "test-key", APIURL: server.URL})
		Expect(err).NotTo(HaveOccurred())
		response, err := provider.Execute(context.Background(), ai.Request{Prompt: api.Prompt{
			User: "I was charged twice for my order.", System: "Use the customer message.", SchemaJSON: json.RawMessage(objectSchema),
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(sent["model"]).To(Equal("jev-latest"))
		Expect(sent["state"]).To(Equal("I was charged twice for my order."))
		questions := sent["questions"].(map[string]any)
		Expect(questions).To(HaveLen(2))
		Expect(questions["route"]).To(HaveKeyWithValue("type", "choice"))
		Expect(questions["route"].(map[string]any)["criteria"]).To(Equal(map[string]any{"billing": nil, "technical": nil}))
		Expect(questions["route"].(map[string]any)["instructions"]).To(ContainSubstring("Select the responsible team"))
		Expect(questions["route"].(map[string]any)["instructions"]).To(ContainSubstring("Use the customer message"))
		Expect(response.Text).To(ContainSubstring(`"value":{"route":"billing","urgency":"high"}`))
		Expect(response.StructuredData).To(MatchJSON(response.Text))
		Expect(response.Usage).To(Equal(api.Usage{InputTokens: 120, OutputTokens: 2}))
		Expect(response.CostUSD).To(BeNumerically(">", 0))
		Expect(response.Runtime).To(Equal(api.Runtime{Provider: "jev", Mode: api.ModeAPI}))
	})

	It("returns a scalar enum as a JSON string", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			Expect(body["questions"].(map[string]any)).To(HaveKey("value"))
			_, err := io.WriteString(w, `{"model":"jev-latest","answers":{"value":{"type":"choice","choice":"approved","confidence":0.9,"probabilities":{"approved":0.9,"rejected":0.1}}},"usage":{"input_tokens":20,"output_tokens":1}}`)
			Expect(err).NotTo(HaveOccurred())
		}))
		DeferCleanup(server.Close)
		provider, err := newConvertedProvider(ai.Config{Model: api.Model{Name: "jev-latest"}, APIKey: "test-key", APIURL: server.URL})
		Expect(err).NotTo(HaveOccurred())
		response, err := provider.Execute(context.Background(), ai.Request{Prompt: api.Prompt{User: "Decision context", SchemaJSON: json.RawMessage(`{"type":"string","enum":["approved","rejected"]}`)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Text).To(ContainSubstring(`"value":"approved"`))
		Expect(response.StructuredData).To(MatchJSON(response.Text))
	})

	DescribeTable("rejects non-enum output schemas before calling Jev", func(schema string, message string) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
		DeferCleanup(server.Close)
		provider, err := newConvertedProvider(ai.Config{Model: api.Model{Name: "jev-latest"}, APIKey: "test-key", APIURL: server.URL})
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.Execute(context.Background(), ai.Request{Prompt: api.Prompt{User: "Context", SchemaJSON: json.RawMessage(schema)}})
		Expect(err).To(MatchError(ContainSubstring(message)))
		Expect(calls.Load()).To(BeZero())
	},
		Entry("missing schema", ``, "output JSON Schema"),
		Entry("free text property", `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`, "answer"),
		Entry("numeric enum", `{"type":"string","enum":[1,2]}`, "string"),
		Entry("open object", `{"type":"object","properties":{"answer":{"type":"string","enum":["yes","no"]}},"required":["answer"]}`, "additionalProperties"),
		Entry("optional property", `{"type":"object","properties":{"answer":{"type":"string","enum":["yes","no"]}},"additionalProperties":false}`, "required"),
	)

	It("rejects a choice outside the requested enum", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.WriteString(w, `{"model":"jev-latest","answers":{"value":{"type":"choice","choice":"unknown","confidence":0.9,"probabilities":{"approved":0.9,"rejected":0.1}}},"usage":{"input_tokens":20,"output_tokens":1}}`)
			Expect(err).NotTo(HaveOccurred())
		}))
		DeferCleanup(server.Close)
		provider, err := newConvertedProvider(ai.Config{Model: api.Model{Name: "jev-latest"}, APIKey: "test-key", APIURL: server.URL})
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.Execute(context.Background(), ai.Request{Prompt: api.Prompt{User: "Decision context", SchemaJSON: json.RawMessage(`{"type":"string","enum":["approved","rejected"]}`)}})
		Expect(err).To(MatchError(ContainSubstring("unknown")))
	})

	It("emits the selected enum and usage in the terminal stream event", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, err := io.WriteString(w, `{"model":"jev-latest","answers":{"value":{"type":"choice","choice":"approved","confidence":0.9,"probabilities":{"approved":0.9,"rejected":0.1}}},"usage":{"input_tokens":20,"output_tokens":1}}`)
			Expect(err).NotTo(HaveOccurred())
		}))
		DeferCleanup(server.Close)
		provider, err := newConvertedProvider(ai.Config{Model: api.Model{Name: "jev-latest"}, APIKey: "test-key", APIURL: server.URL})
		Expect(err).NotTo(HaveOccurred())
		events, err := provider.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{Prompt: api.Prompt{User: "Decision context", SchemaJSON: json.RawMessage(`{"type":"string","enum":["approved","rejected"]}`)}})
		Expect(err).NotTo(HaveOccurred())
		var received []ai.Event
		for event := range events {
			received = append(received, event)
		}
		Expect(received).To(HaveLen(4))
		Expect(received[1].Kind).To(Equal(ai.EventTurn))
		Expect(received[2].Kind).To(Equal(ai.EventText))
		Expect(received[2].Text).To(ContainSubstring(`"value":"approved"`))
		Expect(received[3].Kind).To(Equal(ai.EventResult))
		Expect(received[3].Success).To(BeTrue())
		Expect(received[3].StructuredData).To(MatchJSON(received[2].Text))
		Expect(received[3].Usage).To(Equal(&api.Usage{InputTokens: 20, OutputTokens: 1}))
		Expect(received[3].CostUSD).To(BeNumerically(">", 0))
	})
})
