package tracing

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// RunOptions identifies one agent run.
type RunOptions struct {
	Agent         string
	Scope         string
	MaxIterations int
	BudgetCostUSD float64
}

// RunResult is a finished run's outcome.
type RunResult struct {
	Verified   bool
	Iterations int
	CostUSD    float64
	StopReason string
}

// Run is the root span of one agent run; every model call, tool call and
// database query the run makes nests under it.
type Run struct {
	span trace.Span
}

// StartRun opens an "invoke_agent <agent>" span as a child of ctx.
func StartRun(ctx context.Context, opts RunOptions) (context.Context, *Run) {
	name := opts.Agent
	if name == "" {
		name = "captain"
	}
	ctx, span := tracer().Start(ctx, "invoke_agent "+name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.agent.name", name),
			attribute.String("captain.scope", opts.Scope),
			attribute.Int("captain.max_iterations", opts.MaxIterations),
			attribute.Float64("captain.budget.cost_usd", opts.BudgetCostUSD),
		))
	return ctx, &Run{span: span}
}

// Observe records the session a run joins and each verify verdict it reaches.
func (r *Run) Observe(ev api.Event) {
	switch ev.Kind {
	case api.EventSystem:
		if ev.SessionID != "" {
			r.span.SetAttributes(attribute.String("gen_ai.conversation.id", ev.SessionID))
		}
	case api.EventVerified, api.EventVerifyFailed:
		r.span.AddEvent("captain.verify", trace.WithAttributes(
			attribute.String("captain.verify.name", ev.Tool),
			attribute.Bool("captain.verify.valid", ev.Kind == api.EventVerified),
		))
	}
}

// End closes the run's span with its outcome, marking it failed when err is set.
func (r *Run) End(result RunResult, err error) {
	r.span.SetAttributes(
		attribute.Bool("captain.verified", result.Verified),
		attribute.Int("captain.iterations", result.Iterations),
		attribute.Float64("captain.cost_usd", result.CostUSD),
		attribute.String("captain.stop_reason", result.StopReason),
	)
	if err != nil {
		fail(r.span, err)
	}
	r.span.End()
}
