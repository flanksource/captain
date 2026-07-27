// ABOUTME: The Server interface both mock servers satisfy, plus the listener lifecycle they share.
// ABOUTME: Embedding Base is optional — a server only has to satisfy Server to be usable interchangeably.

package aimock

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
)

// DummyKey is the credential handed to every mocked client. It is deliberately
// obvious in a journal or a process listing: no real key should ever appear.
const DummyKey = "captain-mock"

// MissStatus is the HTTP status returned when no scenario rule matches.
//
// 400 rather than 500 is load-bearing: both agent CLIs treat 5xx as retryable
// (claude retries 10x with exponential backoff, codex 5x), so a 500 turns a
// scenario gap into a minute-long hang instead of an immediate, readable
// failure. 4xx surfaces the diagnostic on the first attempt.
const MissStatus = http.StatusBadRequest

// Server is the common surface both mock servers satisfy. Neither package
// imports the other; this exists so a caller can hold either behind one type.
type Server interface {
	// URL is the base endpoint, e.g. http://127.0.0.1:54321.
	URL() string
	// Env is the KEY=VALUE set that points this protocol's clients at the mock.
	Env() []string
	// Requests is every request served so far — the assertion surface.
	Requests() []Recorded
	Close()
}

// MergeEnv unions Env() across servers, later entries winning on key collision.
// Use it when a test or command runs both mocks at once.
func MergeEnv(servers ...Server) []string {
	merged := map[string]string{}
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		for _, item := range srv.Env() {
			if key, value, ok := strings.Cut(item, "="); ok {
				merged[key] = value
			}
		}
	}
	out := make([]string, 0, len(merged))
	for key, value := range merged {
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out
}

// ExportLines renders env as shell `export` statements, for
// `eval "$(captain ai mock --env ...)"`.
func ExportLines(env []string) string {
	var b strings.Builder
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "export %s=%q\n", key, value)
	}
	return b.String()
}

// Base is the listener lifecycle and journal plumbing both servers share. It is
// optional: embed it to avoid repeating the httptest wiring, or don't and
// satisfy Server directly.
type Base struct {
	server  *httptest.Server
	journal *Journal
}

// Listen binds a server on addr ("" or ":0" picks a free loopback port) and
// starts serving handler. It mirrors mcpproxy.Start: an explicit net.Listen so
// the port is known and bound to loopback, wrapped in httptest.Server for the
// lifecycle.
func (b *Base) Listen(addr string, handler http.Handler, journal *Journal) error {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	b.journal = journal
	b.server = &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	b.server.Start()
	return nil
}

func (b *Base) URL() string {
	if b.server == nil {
		return ""
	}
	return b.server.URL
}

// Requests returns every recorded request.
func (b *Base) Requests() []Recorded {
	if b.journal == nil {
		return nil
	}
	return b.journal.Entries()
}

// Journal exposes the recorder so a handler can append to it.
func (b *Base) Journal() *Journal { return b.journal }

func (b *Base) Close() {
	if b.server != nil {
		b.server.Close()
	}
	if b.journal != nil {
		b.journal.Close()
	}
}
