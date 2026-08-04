package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	commonsctx "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/logger"
)

// withLogLevel pins both the "http" logger and the global logger for the
// duration of a test, because httpTraceLevel resolves the more verbose of the
// two.
func withLogLevel(t *testing.T, level logger.LogLevel) {
	t.Helper()
	h, root := logger.GetLogger("http"), logger.StandardLogger()
	origHTTP, origRoot := h.GetLevel(), root.GetLevel()
	t.Cleanup(func() {
		h.SetLogLevel(origHTTP)
		root.SetLogLevel(origRoot)
	})
	h.SetLogLevel(level)
	root.SetLogLevel(level)
}

// TestHTTPTraceLadder pins the rungs captain advertises: nothing below warn, an
// errors-only access log at the default info level, then access lines, headers,
// request bodies, and response bodies as verbosity climbs.
func TestHTTPTraceLadder(t *testing.T) {
	for _, tc := range []struct {
		name            string
		level           logger.LogLevel
		wantInstalled   bool
		wantErrorsOnly  bool
		wantHeaders     bool
		wantRequestBody bool
		wantResponse    bool
	}{
		{name: "warn installs nothing", level: logger.Warn},
		{name: "info logs failures only", level: logger.Info, wantInstalled: true, wantErrorsOnly: true},
		{name: "-v logs every request", level: logger.Debug, wantInstalled: true},
		{name: "-vv adds headers", level: logger.Trace, wantInstalled: true, wantHeaders: true},
		{name: "-vvv adds request bodies", level: logger.Trace1, wantInstalled: true, wantHeaders: true, wantRequestBody: true},
		{name: "-vvvv adds response bodies", level: logger.Trace2, wantInstalled: true, wantHeaders: true, wantRequestBody: true, wantResponse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withLogLevel(t, tc.level)

			cfg, ok := httpTraceConfig()
			if ok != tc.wantInstalled {
				t.Fatalf("installed = %v, want %v (level %v)", ok, tc.wantInstalled, tc.level)
			}
			base := http.DefaultTransport
			if wrapped := wrapHTTPLogging(base); (wrapped != base) != tc.wantInstalled {
				t.Fatalf("transport wrapped = %v, want %v", wrapped != base, tc.wantInstalled)
			}
			if !ok {
				return
			}
			if !cfg.AccessLog {
				t.Error("every installed rung must keep the access log so failures surface")
			}
			if cfg.AccessLogErrorsOnly != tc.wantErrorsOnly {
				t.Errorf("AccessLogErrorsOnly = %v, want %v", cfg.AccessLogErrorsOnly, tc.wantErrorsOnly)
			}
			if cfg.Headers != tc.wantHeaders || cfg.ResponseHeaders != tc.wantHeaders {
				t.Errorf("Headers/ResponseHeaders = %v/%v, want %v", cfg.Headers, cfg.ResponseHeaders, tc.wantHeaders)
			}
			if cfg.Body != tc.wantRequestBody {
				t.Errorf("Body = %v, want %v", cfg.Body, tc.wantRequestBody)
			}
			if cfg.Response != tc.wantResponse {
				t.Errorf("Response = %v, want %v", cfg.Response, tc.wantResponse)
			}
		})
	}
}

// TestWrapHTTPLoggingRedactsProviderCredentials covers the headers captain
// actually authenticates with: Anthropic sends x-api-key and Gemini
// x-goog-api-key, neither of which httpretty's default sanitizers cover.
func TestWrapHTTPLoggingRedactsProviderCredentials(t *testing.T) {
	const (
		anthropicKey = "sk-ant-api03-REDACTMEANTHROPIC"
		geminiKey    = "AIzaSyD-REDACTMEGEMINI"
		bearerKey    = "sk-proj-REDACTMEOPENAI"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(server.Close)

	withLogLevel(t, logger.Trace)

	var out bytes.Buffer
	ctx := commonsctx.NewContext(context.Background(), commonsctx.WithLogger(logger.NewWithWriter(&out)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("x-api-key", anthropicKey)
	req.Header.Set("x-goog-api-key", geminiKey)
	req.Header.Set("Authorization", "Bearer "+bearerKey)

	resp, err := wrapHTTPLogging(http.DefaultTransport).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	logged := out.String()
	if !strings.Contains(logged, "X-Api-Key") || !strings.Contains(logged, "X-Goog-Api-Key") {
		t.Fatalf("expected both credential headers to be named in the trace, got:\n%s", logged)
	}
	for _, secret := range []string{anthropicKey, geminiKey, bearerKey} {
		if strings.Contains(logged, secret) {
			t.Errorf("credential %q leaked into the HTTP trace:\n%s", secret, logged)
		}
	}
}

// TestErrorsOnlyAccessLogAtDefaultVerbosity covers the rung captain runs at
// with no flags: a failing provider call is reported (with its body, which
// carries the provider's error message) while successful calls stay silent.
func TestErrorsOnlyAccessLogAtDefaultVerbosity(t *testing.T) {
	const providerError = `{"error":{"message":"invalid x-api-key"}}`

	for _, tc := range []struct {
		name       string
		status     int
		wantLogged bool
	}{
		{name: "success stays silent", status: http.StatusOK},
		{name: "failure is reported", status: http.StatusUnauthorized, wantLogged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(providerError))
			}))
			t.Cleanup(server.Close)

			withLogLevel(t, logger.Info)

			var out bytes.Buffer
			ctx := commonsctx.NewContext(context.Background(), commonsctx.WithLogger(logger.NewWithWriter(&out)))
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}

			resp, err := wrapHTTPLogging(http.DefaultTransport).RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			// The access log reads the error body to report it; downstream callers
			// must still see it intact.
			if string(body) != providerError {
				t.Errorf("response body = %q, want it restored to %q", body, providerError)
			}

			logged := out.String()
			if !tc.wantLogged {
				if strings.Contains(logged, server.URL) {
					t.Errorf("a successful request must not be logged at default verbosity, got:\n%s", logged)
				}
				return
			}
			if !strings.Contains(logged, "401") || !strings.Contains(logged, "invalid x-api-key") {
				t.Errorf("expected the failure and its body to be logged, got:\n%s", logged)
			}
		})
	}
}

// TestNamedLoggersAreScoped guards the core benefit of the named-logger
// migration: each subsystem logger carries its own level, so raising one (http)
// does not drag another (cli) to the same verbosity.
func TestNamedLoggersAreScoped(t *testing.T) {
	cliLog := logger.GetLogger("cli")
	httpLog := logger.GetLogger("http")

	origCli, origHTTP := cliLog.GetLevel(), httpLog.GetLevel()
	t.Cleanup(func() {
		cliLog.SetLogLevel(origCli)
		httpLog.SetLogLevel(origHTTP)
	})

	cliLog.SetLogLevel(logger.Info)
	httpLog.SetLogLevel(logger.Trace3)

	if cliLog.IsLevelEnabled(logger.Trace3) {
		t.Fatal("cli logger must stay at info when only the http logger is raised")
	}
	if !httpLog.IsLevelEnabled(logger.Trace3) {
		t.Fatal("http logger must report trace3 enabled after being raised")
	}
}
