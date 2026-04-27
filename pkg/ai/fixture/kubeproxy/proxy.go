// ABOUTME: Reverse proxy that forwards kubectl traffic to the user's cluster.
// ABOUTME: Loads kubeconfig via client-go (so cloud auth/exec plugins work) and logs every API request.

package kubeproxy

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Proxy struct {
	server *httptest.Server
	url    *url.URL

	mu     sync.Mutex
	logger *RequestLogger
}

type RequestLogger struct {
	mu  sync.Mutex
	w   *json.Encoder
	raw interface{ Sync() error }
}

type RequestEvent struct {
	Type     string    `json:"type"`
	Time     time.Time `json:"time"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Query    string    `json:"query,omitempty"`
	Status   int       `json:"status"`
	Duration string    `json:"duration"`
	Bytes    int64     `json:"bytes"`
}

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
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		rp.ServeHTTP(rec, r)
		p.logRequest(r, rec.status, rec.bytes, time.Since(start))
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

func (p *Proxy) logRequest(r *http.Request, status int, bytesOut int64, dur time.Duration) {
	p.mu.Lock()
	logger := p.logger
	p.mu.Unlock()
	if logger == nil {
		return
	}
	logger.LogRequest(RequestEvent{
		Type:     "request",
		Time:     time.Now().UTC(),
		Method:   r.Method,
		Path:     r.URL.Path,
		Query:    r.URL.RawQuery,
		Status:   status,
		Duration: dur.Round(time.Millisecond).String(),
		Bytes:    bytesOut,
	})
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

func NewRequestLogger(f *os.File) *RequestLogger {
	return &RequestLogger{
		w:   json.NewEncoder(f),
		raw: f,
	}
}

func (l *RequestLogger) LogRequest(ev RequestEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.w.Encode(ev)
	if l.raw != nil {
		_ = l.raw.Sync()
	}
}

func (l *RequestLogger) LogCommand(ev CommandEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.w.Encode(ev)
	if l.raw != nil {
		_ = l.raw.Sync()
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

