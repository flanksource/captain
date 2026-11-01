package genkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Genkit schema wiring", func() {
	DescribeTable("repairs rejected Markdown through the native provider", func(ctx SpecContext, buffered bool) {
		inner, server := newBufferedAnthropicProvider(`anthropic:
  - respond:
      text: "I recorded 11 findings in report.mdx."
      usage: {input: 14, output: 8}
  - respond:
      text: '{"status":"ok"}'
      usage: {input: 14, output: 8}
`)
		provider, err := middleware.WithSchemaValidation()(inner)
		Expect(err).NotTo(HaveOccurred())
		req := bufferedAnthropicRequest(api.Prompt{User: "Review the report.", SchemaStrictness: api.SchemaStrictnessRepair,
			SchemaJSON: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"}},"required":["status"],"additionalProperties":false}`),
		})
		var events []ai.Event
		if buffered {
			ctxWithObserver := ai.WithEventObserver(ctx, func(event ai.Event) { events = append(events, event) })
			response, runErr := provider.Execute(ctxWithObserver, req)
			Expect(runErr).NotTo(HaveOccurred())
			Expect(response.Usage).To(Equal(ai.Usage{InputTokens: 28, OutputTokens: 16}))
			Expect(response.StructuredData).To(MatchJSON(`{"status":"ok"}`))
		} else {
			stream, runErr := provider.(ai.StreamingProvider).ExecuteStream(ctx, req)
			Expect(runErr).NotTo(HaveOccurred())
			for event := range stream {
				Expect(event.Kind).NotTo(Equal(ai.EventError), event.Error)
				events = append(events, event)
			}
			Expect(events[len(events)-1].Usage).To(Equal(&ai.Usage{InputTokens: 28, OutputTokens: 16}))
			Expect(events[len(events)-1].StructuredData).To(MatchJSON(`{"status":"ok"}`))
		}
		Expect(events).To(ContainElement(And(HaveField("Rejected", true), HaveField("Text", "I recorded 11 findings in report.mdx."))))
		Expect(server.Remaining()).To(BeEmpty())
	}, Entry("buffered", true), Entry("streamed", false))

	It("sends native JSON constraints with tools for catalog models", func(ctx SpecContext) {
		_, mock := newBufferedAnthropicProvider("anthropic:\n  - respond: {text: '{\"status\":\"ok\"}'}\n")
		target, err := url.Parse(mock.APIURL())
		Expect(err).NotTo(HaveOccurred())
		proxy := httputil.NewSingleHostReverseProxy(target)
		bodies := make(chan map[string]any, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				http.Error(w, readErr.Error(), http.StatusBadRequest)
				return
			}
			var decoded map[string]any
			if decodeErr := json.Unmarshal(body, &decoded); decodeErr != nil {
				http.Error(w, decodeErr.Error(), http.StatusBadRequest)
				return
			}
			bodies <- decoded
			r.Body = io.NopCloser(bytes.NewReader(body))
			proxy.ServeHTTP(w, r)
		}))
		DeferCleanup(server.Close)
		provider, err := New(ai.Config{
			Model:  api.Model{Name: bufferedAnthropicModel, Provider: ai.Anthropic, Mode: api.ModeAPI},
			APIKey: aimock.DummyKey, APIURL: server.URL,
			Tools: []api.ToolDefinition{{Name: "read_document", DefaultPermission: api.ToolPolicyAllow,
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
				Handler:     func(context.Context, map[string]any) (any, error) { return "document", nil },
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = provider.Execute(ctx, bufferedAnthropicRequest(api.Prompt{User: "Review the document.",
			SchemaJSON: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"}},"required":["status"],"additionalProperties":false}`),
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(<-bodies).To(And(
			HaveKeyWithValue("tools", HaveLen(1)),
			HaveKeyWithValue("output_config", And(
				HaveKeyWithValue("effort", "high"),
				HaveKeyWithValue("format", HaveKeyWithValue("type", "json_schema")),
			)),
		))
	})
})
