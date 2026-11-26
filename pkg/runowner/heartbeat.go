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
var heartbeats = &pump{runs: map[uuid.UUID]struct{}{}, interval: HeartbeatInterval}

type pump struct {
	mu       sync.Mutex
	runs     map[uuid.UUID]struct{}
	interval time.Duration
}

// start keeps the claim token holds on runID fresh. A run already pumped (an
// admission replayed in the same process) keeps its one goroutine.
func (p *pump) start(db *database.DB, runID, token uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, pumping := p.runs[runID]; pumping {
		return
	}
	p.runs[runID] = struct{}{}
	go p.beat(db, runID, token)
}

func (p *pump) beat(db *database.DB, runID, token uuid.UUID) {
	defer func() {
		p.mu.Lock()
		delete(p.runs, runID)
		p.mu.Unlock()
	}()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for range ticker.C {
		held, err := db.TouchPromptRunOwner(context.Background(), runID, token)
		if err != nil {
			logger.Warnf("runowner: could not refresh the claim on prompt run %s: %v", runID, err)
			continue
		}
		if !held {
			return
		}
	}
}
