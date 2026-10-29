// Package tracing maps captain's run and model-call lifecycle onto
// OpenTelemetry spans named and attributed per the GenAI semantic conventions.
// Spans carry identifiers, counts and outcomes only — never prompt, response or
// tool content. With no tracer provider installed every span is a no-op.
package tracing

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Name is the instrumentation scope of every captain AI span.
const Name = "github.com/flanksource/captain/ai"

func tracer() trace.Tracer { return otel.Tracer(Name) }

// UsageAttributes renders one response's token buckets and reported cost.
func UsageAttributes(u api.Usage, costUSD float64) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int("gen_ai.usage.input_tokens", u.InputTokens),
		attribute.Int("gen_ai.usage.output_tokens", u.OutputTokens),
		attribute.Int("gen_ai.usage.reasoning_tokens", u.ReasoningTokens),
		attribute.Int("gen_ai.usage.cache_read.input_tokens", u.CacheReadTokens),
		attribute.Int("gen_ai.usage.cache_creation.input_tokens", u.CacheWriteTokens),
		attribute.Float64("captain.cost_usd", costUSD),
	}
}

func fail(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// ChatOptions identifies one model call.
type ChatOptions struct {
	Model    string
	Provider string
	Mode     api.RuntimeMode
	Source   string
}

// Chat is the span of one model call and its tool calls. Observe and End must
// be called from one goroutine.
type Chat struct {
	span  trace.Span
	ctx   context.Context
	tools map[string]toolSpan
}

type toolSpan struct {
	span trace.Span
	ctx  context.Context
}

// StartChat opens a "chat <model>" span as a child of ctx.
func StartChat(ctx context.Context, opts ChatOptions) (context.Context, *Chat) {
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", opts.Model),
		attribute.String("gen_ai.provider.name", opts.Provider),
		attribute.String("captain.mode", string(opts.Mode)),
	}
	if opts.Source != "" {
		attrs = append(attrs, attribute.String("captain.prompt.source", opts.Source))
	}
	ctx, span := tracer().Start(ctx, "chat "+opts.Model,
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
	return ctx, &Chat{span: span, ctx: ctx, tools: map[string]toolSpan{}}
}

// Observe folds one streamed event into the call's span, opening a child span
// per tool call and closing it on the call's result.
func (c *Chat) Observe(ev api.Event) {
	switch ev.Kind {
	case api.EventSystem:
		if ev.SessionID != "" {
			c.span.SetAttributes(attribute.String("gen_ai.conversation.id", ev.SessionID))
		}
	case api.EventToolUse:
		c.startTool(ev)
	case api.EventToolResult:
		c.endTool(ev)
	case api.EventResult:
		c.result(ev.Model, ev.Usage, ev.CostUSD)
	case api.EventError:
		c.result(ev.Model, ev.Usage, ev.CostUSD)
		fail(c.span, errors.New(ev.Error))
	case api.EventInterrupted:
		c.span.AddEvent("captain.interrupted", trace.WithAttributes(attribute.String("captain.reason", ev.Reason)))
	}
}

func (c *Chat) startTool(ev api.Event) {
	if ev.ToolCallID == "" {
		return
	}
	parent := c.ctx
	if p, ok := c.tools[ev.ParentToolCallID]; ok && ev.ParentToolCallID != "" {
		parent = p.ctx
	}
	ctx, span := tracer().Start(parent, "execute_tool "+ev.Tool,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", ev.Tool),
			attribute.String("gen_ai.tool.call.id", ev.ToolCallID),
		))
	c.tools[ev.ToolCallID] = toolSpan{span: span, ctx: ctx}
}

func (c *Chat) endTool(ev api.Event) {
	t, ok := c.tools[ev.ToolCallID]
	if !ok {
		return
	}
	delete(c.tools, ev.ToolCallID)
	if !ev.Success {
		t.span.SetStatus(codes.Error, "tool call failed")
	}
	t.span.End()
}

func (c *Chat) result(model string, usage *api.Usage, costUSD float64) {
	if model != "" {
		c.span.SetAttributes(attribute.String("gen_ai.response.model", model))
	}
	if usage != nil {
		c.span.SetAttributes(UsageAttributes(*usage, costUSD)...)
	}
}

// Response folds a buffered (non-streamed) response into the call's span.
func (c *Chat) Response(resp *api.Response) {
	if resp == nil {
		return
	}
	c.result(resp.Model, &resp.Usage, resp.CostUSD)
	if resp.CacheHit {
		c.span.SetAttributes(attribute.Bool("captain.cache_hit", true))
	}
}

// End closes the call's span, marking it failed when err is set. Tool spans
// still open — a background subagent can outlive the turn — end as incomplete.
func (c *Chat) End(err error) {
	for id, t := range c.tools {
		t.span.SetAttributes(attribute.Bool("captain.incomplete", true))
		t.span.End()
		delete(c.tools, id)
	}
	if err != nil {
		if errors.Is(err, ai.ErrSchemaValidation) {
			c.span.SetAttributes(attribute.String("error.type", "schema_validation"))
		}
		fail(c.span, err)
	}
	c.span.End()
}
