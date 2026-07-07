package ai

import (
	"errors"
	"strings"
)

var (
	ErrBudgetExceeded     = errors.New("budget exceeded")
	ErrCLINotFound        = errors.New("CLI tool not found")
	ErrCLIExecutionFailed = errors.New("CLI execution failed")
	ErrTimeout            = errors.New("operation timed out")
	ErrSchemaValidation   = errors.New("schema validation failed")
	ErrModelNotFound      = errors.New("model not found in pricing registry")
	ErrNoAPIKey           = errors.New("API key not found")
)

// IsRetryable reports whether err is a transient/overload failure worth retrying
// on the same model (see middleware.WithRetry) or falling back to another model
// (see the fallback provider). Provider errors are unstructured strings, so it
// pattern-matches the well-known overload/rate-limit signals; ErrTimeout is
// matched by identity as well.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTimeout) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "timeout")
}
