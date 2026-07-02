package ai

import (
	"context"
	"testing"

	"github.com/flanksource/commons/logger"
)

func TestLoggerFromContext(t *testing.T) {
	attached := logger.GetLogger("ai-context-logger-test-attached")
	fallback := logger.GetLogger("ai-context-logger-test-fallback")

	cases := []struct {
		name string
		ctx  context.Context
		want logger.Logger
	}{
		{
			name: "returns the attached logger when one was set",
			ctx:  ContextWithLogger(context.Background(), attached),
			want: attached,
		},
		{
			name: "returns the fallback when none was attached",
			ctx:  context.Background(),
			want: fallback,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LoggerFromContext(tc.ctx, fallback)
			if got != tc.want {
				t.Fatalf("LoggerFromContext() = %v, want %v", got, tc.want)
			}
		})
	}
}
