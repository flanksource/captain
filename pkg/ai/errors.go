package ai

import (
	"errors"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
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

// ClassifyError normalizes a provider failure into an ErrorClass. Structured
// signals (wrapped sentinels, net.Error, HTTP status) are read before prose.
func ClassifyError(err error) registry.ErrorClass {
	return registry.ClassifyError(err)
}

// IsRetryable reports whether err is a transient/overload failure worth retrying
// on the same model (see middleware.WithRetry) or falling back to another model
// (see the fallback provider).
//
// Classification is shared (registry.ClassifyError) so every caller agrees on
// what "transient" means. It reads structure — wrapped sentinels, net.Error, an
// HTTP status — before falling back to bounded text matching. The previous
// implementation matched `strings.Contains(msg, "429")` against raw prose, so
// "1429 tokens" or "id=4290" read as a rate limit and triggered a pointless
// retry. Note a context-length failure is deliberately NOT retryable: the same
// request fails the same way, so retrying only burns tokens.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTimeout) {
		return true
	}
	return registry.ClassifyError(err).Retryable()
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
	roleIndex := strings.Index(msg, "role")
	notSupportedIndex := strings.Index(msg, "not supported")
	modelIndex := strings.Index(msg, "model")
	if roleIndex >= 0 && notSupportedIndex > roleIndex && (modelIndex < 0 || modelIndex > notSupportedIndex) {
		return false
	}
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
//
// It stays narrower than ErrorAuth on purpose: a rejected key is an auth failure
// but is not "unconfigured", and skipping past it would hide a real problem.
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

// IsContextLengthExceeded reports a request that overflowed the model's context
// window. It is intentionally excluded from IsRetryable and IsFallbackEligible's
// transient set: retrying an oversized request reproduces the failure exactly.
func IsContextLengthExceeded(err error) bool {
	return registry.ClassifyError(err) == registry.ErrorContextLength
}

// IsFallbackEligible reports failures for which trying a different explicitly
// configured model may help. This includes transient provider failures, model
// availability mismatches, missing credentials, and a missing local CLI.
func IsFallbackEligible(err error) bool {
	return IsRetryable(err) || IsModelUnavailable(err) || IsMissingAPIKey(err) || errors.Is(err, ErrCLINotFound)
}
