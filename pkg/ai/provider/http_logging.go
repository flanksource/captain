package provider

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api/icons"
	"github.com/flanksource/commons/logger"
)

type loggingRoundTripper struct {
	inner http.RoundTripper
}

func NewLoggingHTTPClient() *http.Client {
	return &http.Client{
		Transport: &loggingRoundTripper{inner: http.DefaultTransport},
	}
}

func (t *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	logger.Debugf("%v", clicky.Text("").
		Add(icons.Http).
		Append(fmt.Sprintf(" %s ", req.Method), "text-cyan-600 font-medium").
		Append(req.URL.String(), "text-blue-600"))

	if logger.IsTraceEnabled() && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err == nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
			logger.Tracef("%v", clicky.Text("").
				Add(icons.ArrowUp).
				Append(fmt.Sprintf(" request (%d bytes)", len(body)), "text-gray-500").
				NewLine().
				Add(clicky.CodeBlock("json", truncate(string(body), 2000), "max-w-[100ch]")))
		}
	}

	resp, err := t.inner.RoundTrip(req)
	elapsed := time.Since(start)

	if err != nil {
		logger.Debugf("%v", clicky.Text("").
			Add(icons.Error).
			Append(fmt.Sprintf(" %s %s", req.Method, req.URL), "text-red-600").
			Append(fmt.Sprintf(" failed after %v: %v", elapsed, err), "text-red-500"))
		return resp, err
	}

	statusStyle := "text-green-600 font-medium"
	if resp.StatusCode >= 400 {
		statusStyle = "text-red-600 font-medium"
	}
	logger.Debugf("%v", clicky.Text("").
		Add(icons.Http).
		Append(fmt.Sprintf(" %s %s", req.Method, req.URL), "text-gray-500").
		Append(fmt.Sprintf(" %d", resp.StatusCode), statusStyle).
		Append(fmt.Sprintf(" %v", elapsed.Round(time.Millisecond)), "text-gray-400"))

	if logger.IsTraceEnabled() && resp.Body != nil {
		body, err := io.ReadAll(resp.Body)
		if err == nil {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			logger.Tracef("%v", clicky.Text("").
				Add(icons.ArrowDown).
				Append(fmt.Sprintf(" response (%d bytes)", len(body)), "text-gray-500").
				NewLine().
				Add(clicky.CodeBlock("json", truncate(string(body), 2000), "max-w-[100ch]")))
		}
	}

	return resp, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
