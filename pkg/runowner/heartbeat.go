package runowner

import (
	"context"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"
)

// heartbeats is the process's heartbeat pump: one goroutine per claimed run,
// for as long as the claim is this process's and the run is unfinished.
var heartbeats = &pump{runs: map[uuid.UUID]*worker{}, interval: HeartbeatInterval, visibleWithin: HeartbeatTTL}

type pump struct {
	mu       sync.Mutex
	runs     map[uuid.UUID]*worker
	interval time.Duration
	// visibleWithin is how long a claim may stay invisible to a touch: a claim
	// made inside an admission transaction is uncommitted until it ends.
	visibleWithin time.Duration
}

// worker is one run's heartbeat goroutine; finished is set under pump.mu once
// it has stopped maintaining the claim, so a replayed claim replaces it.
type worker struct {
	finished bool
}

// start keeps the claim token holds on runID fresh. A run whose worker is still
// maintaining its claim (an admission replayed in the same process) keeps that
// one goroutine; one whose worker has stopped gets a new worker.
func (p *pump) start(db *database.DB, runID, token uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, pumping := p.runs[runID]; pumping && !existing.finished {
		return
	}
	w := &worker{}
	p.runs[runID] = w
	go p.beat(db, runID, token, w)
}

func (p *pump) beat(db *database.DB, runID, token uuid.UUID, w *worker) {
	defer func() {
		p.mu.Lock()
		w.finished = true
		if p.runs[runID] == w {
			delete(p.runs, runID)
		}
		p.mu.Unlock()
	}()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	startedAt := time.Now()
	visible := false
	for range ticker.C {
		held, err := db.TouchPromptRunOwner(context.Background(), runID, token)
		if err != nil {
			logger.Warnf("runowner: could not refresh the claim on prompt run %s: %v", runID, err)
			continue
		}
		if held {
			visible = true
			continue
		}
		if visible || time.Since(startedAt) >= p.visibleWithin {
			return
		}
	}
}
