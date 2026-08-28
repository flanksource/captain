// ABOUTME: Reverse proxy for HTTP MCP servers — logs every request and lets us inject extra headers.
// ABOUTME: Bearer auth and other client headers are forwarded untouched; we only add what's in the inject map.

package mcpproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/fixture/proxylog"
)

// peekCap is the largest body prefix we read up-front to introspect JSON-RPC
// envelopes. Bodies bigger than this still forward intact (via a MultiReader)
// but only the first peekCap bytes are scanned for method/tool extraction.
const peekCap = 64 * 1024

// RequestLogger is the JSONL writer for proxy events. Aliased from proxylog
// so callers continue to type *mcpproxy.RequestLogger.
type RequestLogger = proxylog.Logger

// NewRequestLogger constructs a JSONL Logger writing to f.
func NewRequestLogger(f *os.File) *RequestLogger {
	return proxylog.NewLogger(f)
}

type Proxy struct {
	server   *httptest.Server
	upstream *url.URL
	name     string
	inject   map[string]string

	mu      sync.Mutex
	logger  *RequestLogger
	observe func(ObservationEvent)
}

type RequestEvent struct {
	Type      string    `json:"type"`
	Time      time.Time `json:"time"`
	Server    string    `json:"server,omitempty"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Query     string    `json:"query,omitempty"`
	Status    int       `json:"status"`
	Duration  string    `json:"duration"`
	Bytes     int64     `json:"bytes"`
	ErrorBody string    `json:"errorBody,omitempty"`

	// JSON-RPC introspection: when the request body parses as a JSON-RPC
	// envelope, RPCMethod is the method ("tools/call", "initialize", etc.)
	// and Tool is the tool name when RPCMethod is "tools/call".
	RPCMethod string `json:"rpcMethod,omitempty"`
	Tool      string `json:"tool,omitempty"`
}

// ObservationEvent is the content-free subset safe for conformance evidence.
type ObservationEvent struct {
	Time       time.Time
	Server     string
	HTTPMethod string
	RPCMethod  string
	Tool       string
	Status     int
	Duration   time.Duration
}

// Start spins up a reverse proxy in front of upstreamURL. Headers in `inject`
// are added to forwarded requests (without removing existing ones — bearer
// tokens and other headers from the client pass through untouched).
func Start(name, upstreamURL string, inject map[string]string) (*Proxy, error) {
	upstream, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream %q: %w", upstreamURL, err)
	}
	if upstream.Scheme == "" || upstream.Host == "" {
		return nil, fmt.Errorf("upstream %q missing scheme or host", upstreamURL)
	}

	p := &Proxy{name: name, upstream: upstream, inject: inject}

	// We expose the proxy URL as <localhost>:<port><upstream.Path>, so the
	// client's outgoing path already matches the upstream's. The director only
	// rewrites scheme/host — leaving r.URL.Path alone avoids double-prefixing
	// when the client follows a relative redirect that strips trailing slashes.
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = upstream.Scheme
			r.URL.Host = upstream.Host
			r.Host = upstream.Host
			for k, v := range p.inject {
				r.Header.Set(k, v)
			}
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcMethod, toolName := peekJSONRPC(r)
		start := time.Now()
		rec := proxylog.NewStatusRecorder(w)
		rp.ServeHTTP(rec, r)
		p.logRequest(r, rec, rpcMethod, toolName, time.Since(start))
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	srv := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	srv.Start()
	p.server = srv
	return p, nil
}

// URL is the proxy endpoint *with the upstream's path appended*. The MCP
// config given to claude points at this URL so the outgoing request path
// already matches the upstream's expected route.
func (p *Proxy) URL() string {
	if p.upstream.Path == "" || p.upstream.Path == "/" {
		return p.server.URL
	}
	return p.server.URL + p.upstream.Path
}

func (p *Proxy) Name() string { return p.name }

func (p *Proxy) Close() {
	if p == nil || p.server == nil {
		return
	}
	p.server.Close()
}

// SetLogger swaps the request log destination. Pass nil to drop logs.
func (p *Proxy) SetLogger(l *RequestLogger) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logger = l
}

// SetObserver installs an in-memory request observer without exposing request
// headers, URLs, queries, or bodies to the observation recorder.
func (p *Proxy) SetObserver(observe func(ObservationEvent)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observe = observe
}

func (p *Proxy) logRequest(r *http.Request, rec *proxylog.StatusRecorder, rpcMethod, tool string, dur time.Duration) {
	p.mu.Lock()
	logger := p.logger
	observe := p.observe
	p.mu.Unlock()
	event := RequestEvent{
		Type:      "request",
		Time:      time.Now().UTC(),
		Server:    p.name,
		Method:    r.Method,
		Path:      r.URL.Path,
		Query:     r.URL.RawQuery,
		Status:    rec.Status,
		Duration:  dur.Round(time.Millisecond).String(),
		Bytes:     rec.Bytes,
		ErrorBody: rec.ErrorBody(),
		RPCMethod: rpcMethod,
		Tool:      tool,
	}
	if logger != nil {
		logger.Write(event)
	}
	if observe != nil {
		observe(ObservationEvent{
			Time: event.Time, Server: event.Server, HTTPMethod: event.Method,
			RPCMethod: event.RPCMethod, Tool: event.Tool, Status: event.Status, Duration: dur,
		})
	}
}

// peekJSONRPC reads up to peekCap bytes of the request body, parses it as a
// JSON-RPC envelope, extracts method (and tool name when method=="tools/call"),
// then resets r.Body so ReverseProxy can still forward it. Bodies larger than
// peekCap are forwarded as-is and only the first peekCap bytes are scanned.
// On any error, r.Body is restored from whatever was already read so we don't
// leave the body half-drained for ServeHTTP.
func peekJSONRPC(r *http.Request) (rpcMethod, tool string) {
	if r.Method != http.MethodPost || r.Body == nil {
		return "", ""
	}
	original := r.Body
	body, _ := io.ReadAll(io.LimitReader(original, peekCap+1))

	// Restore body for the reverse proxy regardless of what happened during
	// the read — anything we already buffered must be replayed first. Even
	// on a partial read, the buffered prefix is enough to extract a JSON-RPC
	// envelope when the method/params fields fit within it.
	if int64(len(body)) > peekCap {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), original), original}
	} else {
		_ = original.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}

	var env struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	scan := body
	if int64(len(scan)) > peekCap {
		scan = scan[:peekCap]
	}
	if err := json.Unmarshal(scan, &env); err != nil {
		return "", ""
	}
	return env.Method, env.Params.Name
}
