package cli

import (
	"net/http"

	"github.com/flanksource/commons/logger"
)

// log is the package-scoped logger for cli diagnostics. Its level follows the
// global -v/--log-level and can be tuned independently with -Plog.level.cli=debug.
var log = logger.GetLogger("cli")

// EnableHTTPWireLogging installs commons' HTTP request/response logger on the
// default transport when the "http" logger is verbose enough. Captain's API
// providers (genkit plugins) issue requests through http.DefaultClient, so
// wrapping the default transport captures their outbound calls with sensitive
// headers (Authorization, Cookie, ...) redacted by commons.
//
// Enable it with -Plog.level.http=trace3 (headers + timing) or trace4 (+bodies).
// Must run before the first provider request; cmd/captain wires it from
// PersistentPreRun, after clicky applies the logging flags.
func EnableHTTPWireLogging() {
	if wrapped := wrapHTTPLogging(http.DefaultTransport); wrapped != http.DefaultTransport {
		http.DefaultTransport = wrapped
		http.DefaultClient = &http.Client{Transport: wrapped}
	}
}

// wrapHTTPLogging returns base wrapped with commons' HTTP logger when the "http"
// logger is at trace3+, otherwise base unchanged. Split out from
// EnableHTTPWireLogging so the level gating is unit-testable without mutating
// http.DefaultTransport.
func wrapHTTPLogging(base http.RoundTripper) http.RoundTripper {
	h := logger.GetLogger("http")
	if !h.IsLevelEnabled(logger.Trace3) {
		return base
	}
	return logger.NewHttpLoggerWithLevels(h, base, logger.Trace3, logger.Trace4)
}
