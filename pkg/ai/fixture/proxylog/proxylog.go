// ABOUTME: Shared HTTP request capture for the kubeproxy and mcpproxy reverse proxies.
// ABOUTME: Provides a JSONL Logger and a StatusRecorder that buffers error response bodies.

package proxylog

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
)

// ErrorBodyCap is how many bytes of a non-2xx response body we capture into
// each request log entry. Plenty for typical "auth failed" / "invalid
// content-type" messages without bloating logs when servers stream back large
// error pages.
const ErrorBodyCap = 4096

// Logger is a concurrency-safe JSONL writer. Each Write flushes through to
// disk so live tailing during a fixture run shows progress.
type Logger struct {
	mu  sync.Mutex
	enc *json.Encoder
	raw interface{ Sync() error }
}

func NewLogger(f *os.File) *Logger {
	return &Logger{enc: json.NewEncoder(f), raw: f}
}

// Write encodes ev as a single JSONL line. Concurrent calls are serialized.
func (l *Logger) Write(ev any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.enc.Encode(ev)
	if l.raw != nil {
		_ = l.raw.Sync()
	}
}

// StatusRecorder wraps http.ResponseWriter to capture the status code, total
// bytes written, and (for non-2xx responses) up to ErrorBodyCap bytes of the
// body for post-mortem debugging.
type StatusRecorder struct {
	http.ResponseWriter
	Status  int
	Bytes   int64
	wrote   bool
	errBody []byte
}

func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
}

func (s *StatusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.Status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *StatusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.Bytes += int64(n)
	if s.Status >= 400 && len(s.errBody) < ErrorBodyCap {
		need := min(ErrorBodyCap-len(s.errBody), n)
		s.errBody = append(s.errBody, b[:need]...)
	}
	return n, err
}

// ErrorBody returns captured response bytes for non-2xx responses, or "" when
// the status was a success. Bounded by ErrorBodyCap.
func (s *StatusRecorder) ErrorBody() string {
	if s.Status < 400 || len(s.errBody) == 0 {
		return ""
	}
	return string(s.errBody)
}
