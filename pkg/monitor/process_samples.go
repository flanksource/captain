package monitor

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

type processKey struct {
	pid       int64
	startedAt time.Time
}

// processBinding is the material part of a process sample: what the row says
// about which session the process is and what it is doing. CPU and memory
// change on nearly every poll, so they ride the heartbeat instead.
type processBinding struct {
	sessionID                    uuid.UUID
	status, command, cwd, source string
}

type processSample struct {
	binding   processBinding
	writtenAt time.Time
}

// processSamples remembers the last sample written per live process, so a poll
// that observes nothing new skips the database. Without it every 5s poll ran
// two transactions per process, and the table carried ten times more dead
// tuples than live rows.
//
// It also remembers what else a steady poll would re-read: the transcript path
// of each live session (immutable once ingest has set it), and the live PID set
// the last vanished-process sweep ran against.
type processSamples struct {
	mu    sync.Mutex
	last  map[processKey]processSample
	paths map[uuid.UUID]string
	// swept is the PID set of the last sweep; nil until a tenure's first sweep.
	swept map[int64]struct{}
}

// reset forgets everything. A fresh lock tenure needs it: another monitor may
// have written the rows in between.
func (s *processSamples) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, s.paths, s.swept = nil, nil, nil
}

// sweepDue reports whether the live PID set differs from the one last swept,
// which is the only way a process row can have vanished since.
func (s *processSamples) sweepDue(alive []int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.swept == nil || len(s.swept) != len(alive) {
		return true
	}
	for _, pid := range alive {
		if _, ok := s.swept[pid]; !ok {
			return true
		}
	}
	return false
}

func (s *processSamples) markSwept(alive []int64) {
	swept := make(map[int64]struct{}, len(alive))
	for _, pid := range alive {
		swept[pid] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swept = swept
}

func (s *processSamples) sessionPath(id uuid.UUID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[id]
}

// rememberPath caches a non-empty path only: an empty one means ingest has not
// claimed the session yet, and must be re-read.
func (s *processSamples) rememberPath(id uuid.UUID, path string) {
	if path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths == nil {
		s.paths = map[uuid.UUID]string{}
	}
	s.paths[id] = path
}

func (s *processSamples) lastWritten(key processKey) (processSample, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sample, ok := s.last[key]
	return sample, ok
}

func (s *processSamples) record(key processKey, sample processSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[processKey]processSample{}
	}
	s.last[key] = sample
}

// retainPIDs drops every remembered process not in alive, and the paths of
// sessions no remaining process is bound to.
func (s *processSamples) retainPIDs(alive []int64) {
	keep := make(map[int64]struct{}, len(alive))
	for _, pid := range alive {
		keep[pid] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bound := map[uuid.UUID]struct{}{}
	for key, sample := range s.last {
		if _, ok := keep[key.pid]; !ok {
			delete(s.last, key)
			continue
		}
		bound[sample.binding.sessionID] = struct{}{}
	}
	for id := range s.paths {
		if _, ok := bound[id]; !ok {
			delete(s.paths, id)
		}
	}
}

// forgetSession drops the samples bound to a session whose process rows were
// closed outside the poll, so the next poll rewrites them.
func (s *processSamples) forgetSession(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, sample := range s.last {
		if sample.binding.sessionID == id {
			delete(s.last, key)
		}
	}
	delete(s.paths, id)
}
