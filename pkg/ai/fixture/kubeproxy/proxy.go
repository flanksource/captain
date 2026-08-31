// ABOUTME: Reverse proxy that forwards kubectl traffic to the user's cluster.
// ABOUTME: Loads kubeconfig via client-go (so cloud auth/exec plugins work) and logs every API request.

package kubeproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/flanksource/captain/pkg/ai/fixture/proxylog"
)

// RequestLogger is the JSONL writer for proxy events. Aliased from proxylog
// so callers continue to type *kubeproxy.RequestLogger.
type RequestLogger = proxylog.Logger

// NewRequestLogger constructs a JSONL Logger writing to f.
func NewRequestLogger(f *os.File) *RequestLogger {
	return proxylog.NewLogger(f)
}

type Proxy struct {
	server *httptest.Server
	url    *url.URL

	mu      sync.Mutex
	logger  *RequestLogger
	observe func(ObservationEvent)
}

type RequestEvent struct {
	Type      string    `json:"type"`
	Time      time.Time `json:"time"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Query     string    `json:"query,omitempty"`
	Status    int       `json:"status"`
	Duration  string    `json:"duration"`
	Bytes     int64     `json:"bytes"`
	ErrorBody string    `json:"errorBody,omitempty"`
}

// ObservationEvent is the content-free subset safe for conformance evidence.
type ObservationEvent struct {
	Time     time.Time
	Method   string
	Resource string
	Status   int
	Duration time.Duration
}

// CommandEvent is left in the public API for downstream callers; the runner
// no longer emits these (kubectl Bash invocations are captured from the
// stream-json output now), but tooling that already parses the JSONL may
// still expect the type.
type CommandEvent struct {
	Type    string    `json:"type"`
	Time    time.Time `json:"time"`
	Command string    `json:"command"`
}

// Start loads kubeconfig (or default discovery if empty), spins up an HTTP
// reverse proxy on a random localhost port, and returns the running Proxy.
// Callers must Close it.
func Start(kubeconfigPath string) (*Proxy, error) {
	cfg, err := loadConfig(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	upstream, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("parse upstream %q: %w", cfg.Host, err)
	}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("auth transport: %w", err)
	}

	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = upstream.Scheme
			r.URL.Host = upstream.Host
			r.Host = upstream.Host
		},
		Transport: transport,
	}

	p := &Proxy{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := proxylog.NewStatusRecorder(w)
		rp.ServeHTTP(rec, r)
		p.logRequest(r, rec, time.Since(start))
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

	listenURL, err := url.Parse(srv.URL)
	if err != nil {
		srv.Close()
		return nil, err
	}
	p.server = srv
	p.url = listenURL
	return p, nil
}

func (p *Proxy) URL() string { return p.server.URL }

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

func (p *Proxy) logRequest(r *http.Request, rec *proxylog.StatusRecorder, dur time.Duration) {
	p.mu.Lock()
	logger := p.logger
	observe := p.observe
	p.mu.Unlock()
	event := RequestEvent{
		Type:      "request",
		Time:      time.Now().UTC(),
		Method:    r.Method,
		Path:      r.URL.Path,
		Query:     r.URL.RawQuery,
		Status:    rec.Status,
		Duration:  dur.Round(time.Millisecond).String(),
		Bytes:     rec.Bytes,
		ErrorBody: rec.ErrorBody(),
	}
	if logger != nil {
		logger.Write(event)
	}
	if observe != nil {
		observe(ObservationEvent{
			Time: event.Time, Method: event.Method, Resource: kubernetesResource(event.Path),
			Status: event.Status, Duration: dur,
		})
	}
}

func kubernetesResource(requestPath string) string {
	segments := strings.FieldsFunc(requestPath, func(r rune) bool { return r == '/' })
	index := -1
	if len(segments) >= 3 && segments[0] == "api" {
		index = 2
	} else if len(segments) >= 4 && segments[0] == "apis" {
		index = 3
	}
	if index < 0 || index >= len(segments) {
		return ""
	}
	if segments[index] == "namespaces" {
		index += 2
	}
	if index >= len(segments) {
		return ""
	}
	resource := segments[index]
	if len(resource) > 128 {
		return ""
	}
	for _, r := range resource {
		if r < 'a' || r > 'z' {
			if r < '0' || r > '9' {
				if r != '.' && r != '-' {
					return ""
				}
			}
		}
	}
	return resource
}

// WriteKubeconfig generates a minimal kubeconfig pointing at the proxy with no
// auth (the proxy injects auth on the way out). Callers should remove the file.
func (p *Proxy) WriteKubeconfig(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	contents := map[string]any{
		"apiVersion":      "v1",
		"kind":            "Config",
		"current-context": "captain-proxy",
		"clusters": []map[string]any{{
			"name": "captain-proxy",
			"cluster": map[string]any{
				"server":                   p.URL(),
				"insecure-skip-tls-verify": true,
			},
		}},
		"contexts": []map[string]any{{
			"name": "captain-proxy",
			"context": map[string]any{
				"cluster": "captain-proxy",
				"user":    "captain-proxy",
			},
		}},
		"users": []map[string]any{{
			"name": "captain-proxy",
			"user": map[string]any{},
		}},
	}
	data, err := yaml.Marshal(contents)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func loadConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}
