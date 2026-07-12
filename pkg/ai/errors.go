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
	ErrModelUnavailable   = errors.New("model unavailable")
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

// IsModelUnavailable reports provider-confirmed model selection failures. It is
// intentionally separate from IsRetryable: retry middleware must not repeat an
// invalid model on the same provider, while an explicit fallback model may
// still recover the request.
func IsModelUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrModelNotFound) || errors.Is(err, ErrSchemaValidation) {
		return false
	}
	if errors.Is(err, ErrModelUnavailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, signal := range []string{
		"model is not supported",
		"model is unsupported",
		"unsupported model",
		"unknown model",
		"model not found",
		"model was not found",
		"model does not exist",
		"model is unavailable",
		"model_not_found",
		"do not have access to model",
		"don't have access to model",
	} {
		if strings.Contains(msg, signal) {
			return true
		}
	}
	if strings.Contains(msg, "invalid model") &&
		!strings.Contains(msg, "model output") &&
		!strings.Contains(msg, "model response") &&
		!strings.Contains(msg, "model schema") {
		return true
	}
	return strings.Contains(msg, "model") &&
		(strings.Contains(msg, "not supported") ||
			strings.Contains(msg, "not found") ||
			strings.Contains(msg, "does not exist") ||
			strings.Contains(msg, "is unavailable"))
}

// IsMissingAPIKey reports missing credentials without treating rejected or
// invalid credentials as recoverable. Cross-backend fallbacks can therefore
// skip an unconfigured API provider without hiding a bad key.
func IsMissingAPIKey(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoAPIKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, signal := range []string{
		"missing api key",
		"no api key",
		"api key is not set",
		"api_key is not set",
	} {
		if strings.Contains(msg, signal) {
			return true
		}
	}
	return false
}

// IsFallbackEligible reports failures for which trying a different explicitly
// configured model may help. This includes transient provider failures, model
// availability mismatches, missing credentials, and a missing local CLI.
func IsFallbackEligible(err error) bool {
	return IsRetryable(err) || IsModelUnavailable(err) || IsMissingAPIKey(err) || errors.Is(err, ErrCLINotFound)
}
