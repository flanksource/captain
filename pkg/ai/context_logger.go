package ai

import (
	"context"

	"github.com/flanksource/commons/logger"
)

// contextLoggerKey is captain's own context key for an attached logger.Logger —
// distinct from commons/context's private logger key, so callers that only hold
// a plain context.Context (not a commons Context) can still carry one (e.g. a
// clicky *task.Task, which implements logger.Logger and buffers into the task's
// own trace log).
type contextLoggerKey struct{}

// ContextWithLogger attaches log to ctx, retrievable via LoggerFromContext.
func ContextWithLogger(ctx context.Context, log logger.Logger) context.Context {
	return context.WithValue(ctx, contextLoggerKey{}, log)
}

// LoggerFromContext returns the logger attached via ContextWithLogger, or
// fallback when ctx carries none.
func LoggerFromContext(ctx context.Context, fallback logger.Logger) logger.Logger {
	if log, ok := ctx.Value(contextLoggerKey{}).(logger.Logger); ok && log != nil {
		return log
	}
	return fallback
}
