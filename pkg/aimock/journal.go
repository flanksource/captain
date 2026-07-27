// ABOUTME: In-memory request journal plus an optional JSONL sink, reusing the proxylog writer.
// ABOUTME: Requests() is the assertion surface: what the agent actually sent, not what we think it sent.

package aimock

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/fixture/proxylog"
)

// Recorded is one request the mock served.
type Recorded struct {
	Type     string    `json:"type"`
	Time     time.Time `json:"time"`
	Protocol string    `json:"protocol"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Status   int       `json:"status"`
	Stream   bool      `json:"stream"`
	Model    string    `json:"model,omitempty"`
	Request  Request   `json:"request"`
	// Miss is the diagnostic for a request that produced no clean scripted
	// reply — no rule matched, the route is unimplemented, or the stream aborted
	// mid-flight. Empty on a normal request.
	Miss string `json:"miss,omitempty"`
}

// Journal records every served request in memory and, when opened with a path,
// mirrors each entry to a JSONL file for post-mortem inspection of a CLI run.
type Journal struct {
	protocol string

	mu       sync.Mutex
	entries  []Recorded
	logger   *proxylog.Logger
	file     *os.File
	fileOnce sync.Once
}

// NewJournal opens a journal for a protocol. An empty path keeps records in
// memory only.
func NewJournal(protocol, path string) (*Journal, error) {
	j := &Journal{protocol: protocol}
	if path == "" {
		return j, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open journal %s: %w", path, err)
	}
	j.file = file
	j.logger = proxylog.NewLogger(file)
	return j, nil
}

// Record appends an entry, stamping the protocol and record type.
func (j *Journal) Record(entry Recorded) {
	entry.Type = "request"
	entry.Protocol = j.protocol
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}

	j.mu.Lock()
	j.entries = append(j.entries, entry)
	logger := j.logger
	j.mu.Unlock()

	if logger != nil {
		logger.Write(entry)
	}
}

// Entries returns a copy of everything recorded so far.
func (j *Journal) Entries() []Recorded {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Recorded(nil), j.entries...)
}

// Close flushes and closes the JSONL sink, if any.
func (j *Journal) Close() {
	j.fileOnce.Do(func() {
		if j.file != nil {
			_ = j.file.Close()
		}
	})
}
