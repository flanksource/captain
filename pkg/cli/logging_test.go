package cli

import (
	"net/http"
	"testing"

	"github.com/flanksource/commons/logger"
)

// TestWrapHTTPLogging verifies the level gate: the default transport is only
// wrapped once the "http" logger reaches trace3, which is how
// -Plog.level.http=trace3 enables redacted HTTP wire logging.
func TestWrapHTTPLogging(t *testing.T) {
	h := logger.GetLogger("http")
	orig := h.GetLevel()
	t.Cleanup(func() { h.SetLogLevel(orig) })

	base := http.DefaultTransport

	h.SetLogLevel(logger.Info)
	if got := wrapHTTPLogging(base); got != base {
		t.Fatalf("at info level the transport must be returned unwrapped, got %T", got)
	}

	h.SetLogLevel(logger.Debug)
	if got := wrapHTTPLogging(base); got != base {
		t.Fatalf("at debug level (below trace3) the transport must stay unwrapped, got %T", got)
	}

	h.SetLogLevel(logger.Trace3)
	if got := wrapHTTPLogging(base); got == base {
		t.Fatal("at trace3 the transport must be wrapped with the commons HTTP logger")
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
