package agent

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// spanningProvider opens a span from the context it is called with, standing in
// for the tracing middleware so the run span's propagation is observable.
type spanningProvider struct{ fakeProvider }

func (p *spanningProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	_, span := otel.Tracer("test").Start(ctx, "model call")
	span.End()
	return p.fakeProvider.ExecuteStream(ctx, req)
}

// emittingVerifier reports its verdict on the run's stream, as verify hooks do.
type emittingVerifier struct{ valid bool }

func (emittingVerifier) Name() string { return "fixture" }
func (v emittingVerifier) Verify(hc *HookContext) (VerifyResult, error) {
	kind := ai.EventVerifyFailed
	if v.valid {
		kind = ai.EventVerified
	}
	hc.Emit(ai.Event{Kind: kind, Tool: "fixture", Text: "report"})
	return VerifyResult{Valid: v.valid}, nil
}

type failingPreRun struct{}

func (failingPreRun) Name() string               { return "setup" }
func (failingPreRun) PreRun(*HookContext) error { return errors.New("checkout failed") }

var _ = Describe("Runner tracing", func() {
	const (
		source    = "review"
		sessionID = "session-1"
		costUSD   = 0.25
		budgetUSD = 2.0
	)
	var recorder *tracetest.SpanRecorder

	BeforeEach(func() {
		recorder = tracetest.NewSpanRecorder()
		DeferCleanup(otel.SetTracerProvider, otel.GetTracerProvider())
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	})

	request := ai.Request{
		Prompt: api.Prompt{User: "go", Source: source},
		Budget: api.Budget{Cost: budgetUSD},
	}

	ended := func(name string) sdktrace.ReadOnlySpan {
		for _, s := range recorder.Ended() {
			if s.Name() == name {
				return s
			}
		}
		Fail("no ended span named " + name)
		return nil
	}
	attrs := func(s sdktrace.ReadOnlySpan) map[string]any {
		out := map[string]any{}
		for _, kv := range s.Attributes() {
			out[string(kv.Key)] = kv.Value.AsInterface()
		}
		return out
	}

	It("wraps the run in an invoke_agent span parenting each model call", func() {
		prov := &spanningProvider{fakeProvider{events: func(int) []ai.Event {
			return []ai.Event{
				{Kind: ai.EventSystem, SessionID: sessionID},
				{Kind: ai.EventResult, Success: true, CostUSD: costUSD},
			}
		}}}
		r := &Runner[string]{Provider: prov, Request: request, Hooks: []any{emittingVerifier{valid: true}}}
		_, err := r.Run(context.Background())
		Expect(err).NotTo(HaveOccurred())

		run := ended("invoke_agent " + source)
		Expect(ended("model call").Parent().SpanID()).To(Equal(run.SpanContext().SpanID()))
		Expect(attrs(run)).To(Equal(map[string]any{
			"gen_ai.operation.name":   "invoke_agent",
			"gen_ai.agent.name":       source,
			"gen_ai.conversation.id":  sessionID,
			"captain.scope":           string(ScopeAll),
			"captain.max_iterations":  int64(1),
			"captain.budget.cost_usd": budgetUSD,
			"captain.verified":        true,
			"captain.iterations":      int64(1),
			"captain.cost_usd":        costUSD,
			"captain.stop_reason":     "condition-met",
		}))
		Expect(run.Status().Code).To(Equal(codes.Unset))
		Expect(run.Events()).To(HaveLen(1))
		verify := run.Events()[0]
		Expect(verify.Name).To(Equal("captain.verify"))
		Expect(verify.Attributes).To(ConsistOf(
			HaveField("Key", BeEquivalentTo("captain.verify.name")),
			HaveField("Key", BeEquivalentTo("captain.verify.valid")),
		))
	})

	It("marks the run span failed when a PreRun hook fails", func() {
		r := &Runner[string]{Provider: &fakeProvider{}, Request: request, Hooks: []any{failingPreRun{}}}
		_, err := r.Run(context.Background())
		Expect(err).To(MatchError(ContainSubstring("checkout failed")))

		run := ended("invoke_agent " + source)
		Expect([]any{run.Status().Code, attrs(run)["captain.verified"], attrs(run)["captain.iterations"]}).
			To(Equal([]any{codes.Error, true, int64(0)}))
	})
})
