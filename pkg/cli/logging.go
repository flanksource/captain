package cli

import (
	"net/http"
	"sync"

	"github.com/flanksource/commons/har"
	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/http/middlewares"
	"github.com/flanksource/commons/logger"
)

// log is the package-scoped logger for cli diagnostics. Its level follows the
// global -v/--log-level and can be tuned independently with -Plog.level.cli=debug.
var log = logger.GetLogger("cli")

var httpLoggingOnce sync.Once

// EnableHTTPWireLogging installs commons' HTTP trace middleware on the default
// transport. Captain's API providers (genkit plugins) and its own fetchers
// (pkg/ai/models_remote.go, pkg/ai/pricing/openrouter.go) all issue requests
// through http.DefaultClient, so wrapping the default transport is what
// captures their outbound calls.
//
// commons has the same hook built in (logger.onPropertyUpdate wraps
// http.DefaultTransport for log.level.http), but it never fires here: the
// listener is registered by logger.UseSlog(), which captain does not call, and
// -P values are bound straight into properties.commandlineProperties without
// going through Set/Update, so no listener is notified. Do not delete this in
// favour of the commons hook — and if captain ever adopts UseSlog(), the Once
// below is what keeps a second printer from stacking onto the first.
//
// The same transport carries HAR capture when -Phttp.har=<path> is set; the
// archive is written by FlushHAR once the command finishes.
//
// Must run before the first provider request; cmd/captain wires it from
// PersistentPreRun, after clicky applies the logging flags.
func EnableHTTPWireLogging() error {
	var err error
	httpLoggingOnce.Do(func() {
		var captured http.RoundTripper
		// HAR sits innermost, closest to the transport, matching the ordering
		// commons/http uses so the archive records the request as it went out.
		if captured, err = harRegistry.Transport(harFeature, http.DefaultTransport); err != nil {
			return
		}
		if wrapped := wrapHTTPLogging(captured); wrapped != http.DefaultTransport {
			http.DefaultTransport = wrapped
			http.DefaultClient = &http.Client{Transport: wrapped}
		}
	})
	return err
}

// harFeature names captain's traffic for per-subsystem overrides, so
// -Phttp.captain.har=<path> and the global -Phttp.har=<path> both resolve.
const harFeature = "captain"

// harRegistry resolves -Phttp.har* into collectors. It is process-wide because
// http.DefaultTransport is.
var harRegistry = har.NewRegistry()

// FlushHAR writes any archive requested with -Phttp.har=<path>. cmd/captain
// calls it after Execute returns — including on the error path, which is the
// run most worth capturing.
func FlushHAR() {
	if err := harRegistry.Flush(log); err != nil {
		log.Errorf("%v", err)
	}
}

// wrapHTTPLogging returns base wrapped with the commons trace middleware, or
// base unchanged when the resolved level is too low to log anything. Split out
// from EnableHTTPWireLogging so the ladder is unit-testable without mutating
// http.DefaultTransport.
func wrapHTTPLogging(base http.RoundTripper) http.RoundTripper {
	cfg, ok := httpTraceConfig()
	if !ok {
		return base
	}
	return middlewares.NewLogger(cfg)(base)
}

// httpTraceConfig maps the effective HTTP log level onto commons' trace ladder,
// which is relative to a base level (Debug by default, overridable with
// -Phttp.log.base-level or HTTP_LOG_BASE_LEVEL):
//
//	warn and below  nothing is installed
//	info            failed requests only (status >= 400 or transport error)
//	-v              an access line per request
//	-vv             + request/response headers, query and form params
//	-vvv            + request bodies, TLS summary
//	-vvvv           + response bodies
func httpTraceConfig() (commonshttp.TraceConfig, bool) {
	cfg, ok := commonshttp.TraceConfigForLogLevel(httpTraceLevel())
	if !ok {
		return cfg, false
	}
	// commons redacts these already via CommonRedactedHeaders ("*-Key"); naming
	// captain's own credential headers keeps that guarantee explicit, since the
	// Anthropic and Gemini fetchers authenticate with them rather than Bearer.
	cfg.RedactedHeaders = append(cfg.RedactedHeaders, "x-api-key", "x-goog-api-key")
	return cfg, true
}

// httpTraceLevel resolves the level the ladder is built from. Named loggers
// take their level from --log-level/-Plog.level.http, while the -v count is
// applied to the global logger, so the effective level is the more verbose of
// the two.
func httpTraceLevel() logger.LogLevel {
	level := logger.GetLogger("http").GetLevel()
	if root := logger.StandardLogger().GetLevel(); root > level {
		level = root
	}
	return level
}
