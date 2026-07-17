package registry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
)

// statusErr is a provider error carrying a real HTTP status.
type statusErr struct {
	code int
	msg  string
}

func (e statusErr) Error() string   { return e.msg }
func (e statusErr) StatusCode() int { return e.code }

// TestClassifyErrorReadsStructureNotProse pins that a real status code decides
// the class even when the message says nothing about it.
func TestClassifyErrorReadsStructureNotProse(t *testing.T) {
	cases := []struct {
		code int
		want ErrorClass
	}{
		{429, ErrorRateLimit},
		{503, ErrorOverloaded},
		{529, ErrorOverloaded},
		{500, ErrorOverloaded},
		{401, ErrorAuth},
		{403, ErrorAuth},
		{404, ErrorModelUnavailable},
		{400, ErrorInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			err := fmt.Errorf("generate: %w", statusErr{code: tc.code, msg: "provider said no"})
			if got := ClassifyError(err); got != tc.want {
				t.Errorf("ClassifyError(status %d) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

// TestClassifyErrorDoesNotMatchNumbersInProse is the regression this refactor
// exists to prevent. The previous implementation did
// strings.Contains(msg, "429"), so any message that merely contained those
// digits — a token count, an id, a byte offset — was treated as a rate limit and
// retried for nothing.
func TestClassifyErrorDoesNotMatchNumbersInProse(t *testing.T) {
	falsePositives := []string{
		"request rejected: 1429 tokens exceeds nothing in particular",
		"model gpt-429-turbo is fine",
		"trace id=4290 completed",
		"finished after 5031 ms",
		"embedding dimension 15039 mismatch",
	}
	for _, msg := range falsePositives {
		t.Run(msg, func(t *testing.T) {
			if got := ClassifyError(errors.New(msg)); got.Retryable() {
				t.Errorf("ClassifyError(%q) = %q, which retries; digits inside prose are not a status code", msg, got)
			}
		})
	}

	// A real status in prose still classifies: the boundary is the point, not
	// refusing to read text at all.
	for msg, want := range map[string]ErrorClass{
		"provider returned 429 Too Many Requests": ErrorRateLimit,
		"upstream 503 service unavailable":        ErrorOverloaded,
		"HTTP 401 unauthorized":                   ErrorAuth,
	} {
		if got := ClassifyError(errors.New(msg)); got != want {
			t.Errorf("ClassifyError(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestClassifyErrorNetworkAndContext(t *testing.T) {
	cases := map[string]struct {
		err  error
		want ErrorClass
	}{
		"deadline":     {context.DeadlineExceeded, ErrorNetwork},
		"canceled":     {context.Canceled, ErrorNetwork},
		"url error":    {&url.Error{Op: "Post", URL: "https://api.example", Err: errors.New("dial tcp: connect: connection refused")}, ErrorNetwork},
		"net error":    {&net.OpError{Op: "dial", Err: errors.New("connection refused")}, ErrorNetwork},
		"reset":        {errors.New("read: connection reset by peer"), ErrorNetwork},
		"context len":  {errors.New("This model's maximum context length is 200000 tokens"), ErrorContextLength},
		"ctx len code": {errors.New("context_length_exceeded"), ErrorContextLength},
		"prompt long":  {errors.New("prompt is too long: 250000 tokens > 200000 maximum"), ErrorContextLength},
		"nil":          {nil, ErrorNone},
		"unknown":      {errors.New("something else entirely"), ErrorNone},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ClassifyError(tc.err); got != tc.want {
				t.Errorf("ClassifyError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestContextLengthIsNotRetryable: an oversized request fails identically on
// every retry, so retrying it only spends tokens to reach the same error.
func TestContextLengthIsNotRetryable(t *testing.T) {
	if ErrorContextLength.Retryable() {
		t.Error("ErrorContextLength must not be retryable: the same request fails the same way")
	}
	// A context-length message that also contains a token count must not be
	// rescued into a retryable class by the digits it carries.
	err := errors.New("prompt is too long: 429000 tokens > 200000 maximum")
	if got := ClassifyError(err); got != ErrorContextLength {
		t.Errorf("ClassifyError(%v) = %q, want %q; the token count must not win over the real cause", err, got, ErrorContextLength)
	}
}

func TestRetryableClasses(t *testing.T) {
	retryable := map[ErrorClass]bool{
		ErrorRateLimit:        true,
		ErrorOverloaded:       true,
		ErrorNetwork:          true,
		ErrorContextLength:    false,
		ErrorAuth:             false,
		ErrorModelUnavailable: false,
		ErrorInvalidRequest:   false,
		ErrorNone:             false,
	}
	for class, want := range retryable {
		if got := class.Retryable(); got != want {
			t.Errorf("%q.Retryable() = %v, want %v", class, got, want)
		}
	}
}
