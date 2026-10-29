package middleware

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// eventStubProvider streams a fixed event sequence.
type eventStubProvider struct {
	stubProvider
	events []ai.Event
}

func (s eventStubProvider) ExecuteStream(context.Context, ai.Request) (<-chan ai.Event, error) {
	ch := make(chan ai.Event)
	go func() {
		defer close(ch)
		for _, ev := range s.events {
			ch <- ev
		}
	}()
	return ch, nil
}

type spanSummary struct {
	Name, Parent string
	Status       codes.Code
	Attrs        map[string]any
}

// summarize flattens recorded spans into name, parent name, status and every
// attribute, so a whole span tree compares in one assertion.
func summarize(spans []sdktrace.ReadOnlySpan) []spanSummary {
	names := map[string]string{}
	for _, s := range spans {
		names[s.SpanContext().SpanID().String()] = s.Name()
	}
	out := make([]spanSummary, 0, len(spans))
	for _, s := range spans {
		attrs := map[string]any{}
		for _, kv := range s.Attributes() {
			attrs[string(kv.Key)] = kv.Value.AsInterface()
		}
		out = append(out, spanSummary{
			Name:   s.Name(),
			Parent: names[s.Parent().SpanID().String()],
			Status: s.Status().Code,
			Attrs:  attrs,
		})
	}
	return out
}

func recordSpans() *tracetest.SpanRecorder {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	DeferCleanup(otel.SetTracerProvider, previous)
	return recorder
}

func drain(ch <-chan ai.Event) []ai.Event {
	var events []ai.Event
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

var _ = Describe("WithTracing", func() {
	const (
		source    = "review"
		sessionID = "session-1"
		respModel = "test-model-2026"
		costUSD   = 0.25
	)
	stub := stubProvider{}
	provider := stub.GetRuntime().Provider
	mode := string(stub.GetRuntime().Mode)
	request := ai.Request{Prompt: api.Prompt{Source: source}}

	chatAttrs := func(extra map[string]any) map[string]any {
		attrs := map[string]any{
			"gen_ai.operation.name": "chat",
			"gen_ai.request.model":  "test-model",
			"gen_ai.provider.name":  provider,
			"captain.mode":          mode,
			"captain.prompt.source": source,
		}
		for k, v := range extra {
			attrs[k] = v
		}
		return attrs
	}
	toolAttrs := func(name, id string, extra map[string]any) map[string]any {
		attrs := map[string]any{
			"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name":      name,
			"gen_ai.tool.call.id":   id,
		}
		for k, v := range extra {
			attrs[k] = v
		}
		return attrs
	}
	usageAttrs := map[string]any{
		"gen_ai.response.model":                    respModel,
		"gen_ai.conversation.id":                   sessionID,
		"gen_ai.usage.input_tokens":                int64(10),
		"gen_ai.usage.output_tokens":               int64(5),
		"gen_ai.usage.reasoning_tokens":            int64(0),
		"gen_ai.usage.cache_read.input_tokens":     int64(3),
		"gen_ai.usage.cache_creation.input_tokens": int64(0),
		"captain.cost_usd":                         costUSD,
	}

	It("streams a chat span with nested tool spans and forwards every event", func() {
		recorder := recordSpans()
		events := []ai.Event{
			{Kind: ai.EventSystem, SessionID: sessionID},
			{Kind: ai.EventToolUse, Tool: "Agent", ToolCallID: "a"},
			{Kind: ai.EventToolUse, Tool: "Bash", ToolCallID: "b", ParentToolCallID: "a"},
			{Kind: ai.EventToolResult, ToolCallID: "b", Success: true},
			{Kind: ai.EventToolResult, ToolCallID: "a", Success: false},
			{Kind: ai.EventToolUse, Tool: "Read", ToolCallID: "c"},
			{Kind: ai.EventResult, Model: respModel, Usage: &api.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3}, CostUSD: costUSD},
		}
		p, err := WithTracing()(eventStubProvider{events: events})
		Expect(err).NotTo(HaveOccurred())
		ch, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())

		Expect(drain(ch)).To(Equal(events))
		Expect(summarize(recorder.Ended())).To(Equal([]spanSummary{
			{Name: "execute_tool Bash", Parent: "execute_tool Agent", Status: codes.Unset, Attrs: toolAttrs("Bash", "b", nil)},
			{Name: "execute_tool Agent", Parent: "chat test-model", Status: codes.Error, Attrs: toolAttrs("Agent", "a", nil)},
			{Name: "execute_tool Read", Parent: "chat test-model", Status: codes.Unset, Attrs: toolAttrs("Read", "c", map[string]any{"captain.incomplete": true})},
			{Name: "chat test-model", Status: codes.Unset, Attrs: chatAttrs(usageAttrs)},
		}))
	})

	It("marks a stream that reports an error as failed", func() {
		recorder := recordSpans()
		p, err := WithTracing()(eventStubProvider{events: []ai.Event{{Kind: ai.EventError, Error: "rate limited"}}})
		Expect(err).NotTo(HaveOccurred())
		ch, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		drain(ch)

		spans := recorder.Ended()
		Expect(spans).To(HaveLen(1))
		Expect([]any{spans[0].Status().Code, spans[0].Status().Description}).To(Equal([]any{codes.Error, "rate limited"}))
	})

	It("drains and closes the stream once the caller's context is cancelled", func() {
		recorder := recordSpans()
		p, err := WithTracing()(eventStubProvider{events: []ai.Event{{Kind: ai.EventText, Text: "one"}, {Kind: ai.EventText, Text: "two"}}})
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		ch, err := p.(ai.StreamingProvider).ExecuteStream(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		cancel()

		Eventually(recorder.Ended).Should(HaveLen(1))
		Expect(recorder.Ended()[0].Status().Code).To(Equal(codes.Error))
		Eventually(ch).Should(BeClosed())
	})

	It("records a buffered call's usage and its schema-validation failure", func() {
		recorder := recordSpans()
		resp := &ai.Response{Model: respModel, Usage: api.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3}, CostUSD: costUSD}
		p, err := WithTracing()(stubProvider{resp: resp, err: fmt.Errorf("reply: %w", ai.ErrSchemaValidation)})
		Expect(err).NotTo(HaveOccurred())
		_, err = p.Execute(context.Background(), request)
		Expect(err).To(MatchError(ai.ErrSchemaValidation))

		attrs := chatAttrs(usageAttrs)
		delete(attrs, "gen_ai.conversation.id")
		attrs["error.type"] = "schema_validation"
		Expect(summarize(recorder.Ended())).To(Equal([]spanSummary{
			{Name: "chat test-model", Status: codes.Error, Attrs: attrs},
		}))
	})
})
