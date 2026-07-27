package registry

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// ErrorClass is what went wrong with a provider call, normalized across
// providers so retry, fallback, and budget logic can reason about it without
// re-reading error prose.
type ErrorClass string

const (
	// ErrorNone means "not a recognized provider failure".
	ErrorNone ErrorClass = ""
	// ErrorRateLimit is a 429 / quota exhaustion. Retryable, ideally after a wait.
	ErrorRateLimit ErrorClass = "rate_limit"
	// ErrorOverloaded is a 503/529 or explicit overload. Retryable.
	ErrorOverloaded ErrorClass = "overloaded"
	// ErrorNetwork is a transport failure: dial, reset, EOF, deadline. Retryable.
	ErrorNetwork ErrorClass = "network"
	// ErrorContextLength means the request exceeded the model's context window.
	// NOT retryable: the identical request fails identically, so a retry only
	// burns tokens. Recoverable by trimming input or moving to a larger model.
	ErrorContextLength ErrorClass = "context_length"
	// ErrorAuth is a missing/rejected credential. Not retryable.
	ErrorAuth ErrorClass = "auth"
	// ErrorModelUnavailable is a provider-confirmed model selection failure. Not
	// retryable on the same model; an explicit fallback may still recover.
	ErrorModelUnavailable ErrorClass = "model_unavailable"
	// ErrorInvalidRequest is a malformed request. Not retryable.
	ErrorInvalidRequest ErrorClass = "invalid_request"
)

// Retryable reports whether the same request may succeed if tried again.
func (c ErrorClass) Retryable() bool {
	switch c {
	case ErrorRateLimit, ErrorOverloaded, ErrorNetwork:
		return true
	default:
		return false
	}
}

// httpStatusError is implemented by errors that carry a real HTTP status. Status
// is read structurally where available, because reading it out of prose is how
// captain used to classify "1429 tokens" as a rate limit.
type httpStatusError interface{ StatusCode() int }

// Status codes must stand alone. `strings.Contains(msg, "429")` — the previous
// implementation — also matched "1429 tokens", "id=4290", and "request 5031",
// turning ordinary messages into spurious retries.
//
// \b is not enough either: it treats "-" and "." as boundaries, so "gpt-429-turbo"
// and version strings like "4.29.1" would still match. The delimiter set
// therefore excludes word characters, "." and "-", leaving whitespace,
// punctuation, and string edges.
const (
	statusPrefix = `(^|[^\w.-])`
	statusSuffix = `([^\w.-]|$)`
)

var (
	reRateLimitStatus  = regexp.MustCompile(statusPrefix + `429` + statusSuffix)
	reOverloadedStatus = regexp.MustCompile(statusPrefix + `(503|529)` + statusSuffix)
	reAuthStatus       = regexp.MustCompile(statusPrefix + `(401|403)` + statusSuffix)
)

// ClassifyError applies the provider-independent heuristics: structured signals
// first (wrapped sentinels, net.Error, url.Error, HTTP status), then bounded
// text matching. Providers refine what is left via Provider.ClassifyError.
func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ErrorNone
	}

	// 1. Structure beats prose.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrorNetwork
	}
	var status httpStatusError
	if errors.As(err, &status) {
		switch code := status.StatusCode(); {
		case code == 429:
			return ErrorRateLimit
		case code == 503 || code == 529:
			return ErrorOverloaded
		case code == 401 || code == 403:
			return ErrorAuth
		case code == 404:
			return ErrorModelUnavailable
		case code >= 500:
			return ErrorOverloaded
		case code == 400 || code == 422:
			return ErrorInvalidRequest
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return ErrorNetwork
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return ErrorNetwork
	}

	// 2. Bounded text matching for providers that only return prose.
	msg := strings.ToLower(err.Error())
	switch {
	case containsAny(msg, "context length", "context window", "maximum context",
		"context_length_exceeded", "too many tokens", "prompt is too long",
		"request too large", "string too long"):
		return ErrorContextLength
	case reRateLimitStatus.MatchString(msg), strings.Contains(msg, "rate limit"),
		strings.Contains(msg, "rate_limit"), strings.Contains(msg, "quota exceeded"),
		strings.Contains(msg, "resource_exhausted"), strings.Contains(msg, "too many requests"):
		return ErrorRateLimit
	case reOverloadedStatus.MatchString(msg), strings.Contains(msg, "overloaded"),
		strings.Contains(msg, "server_error"), strings.Contains(msg, "service unavailable"),
		strings.Contains(msg, "try again later"):
		return ErrorOverloaded
	case containsAny(msg, "timeout", "timed out", "connection reset", "connection refused",
		"broken pipe", "unexpected eof", "no such host", "tls handshake"):
		return ErrorNetwork
	case reAuthStatus.MatchString(msg), containsAny(msg, "unauthorized", "invalid api key",
		"invalid_api_key", "authentication", "permission denied", "missing api key",
		"no api key", "api key is not set", "api_key is not set"):
		return ErrorAuth
	}
	return ErrorNone
}

// ClassifyError refines the shared verdict with this provider's own error
// vocabulary. Providers set classifyErr only where their wording is genuinely
// their own; everything common stays in the shared heuristics.
func (p *Provider) ClassifyError(err error) ErrorClass {
	base := ClassifyError(err)
	if err == nil || p.classifyErr == nil {
		return base
	}
	return p.classifyErr(err, base)
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
