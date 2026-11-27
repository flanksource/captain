package cli

import (
	"sync"
	"time"

	"github.com/flanksource/clicky/shutdown"
)

// promptRunSettleTimeout bounds how long an exiting host waits for the async
// runs it drives to settle. A run that outlasts it is left to the next
// admission or orphan sweep, which reclaims it once this process is gone.
const promptRunSettleTimeout = 5 * time.Second

var stopPromptRunsHook sync.Once

// stopPromptRunsOnShutdown has the process's shutdown hooks stop the async runs
// it drives: an embedding host that exits while a run is in flight would
// otherwise leave it unsettled, holding its session, until it is reclaimed.
func stopPromptRunsOnShutdown() {
	stopPromptRunsHook.Do(func() {
		shutdown.AddHook("stop prompt runs", func() { StopPromptRuns(promptRunSettleTimeout) })
	})
}

// StopPromptRuns stops every async prompt run this process is driving and
// waits up to timeout for each to settle its record, reporting how many did
// not. A stopped run settles as cancelled through its own recorder; one whose
// agent the shutdown signal already killed settles as failed.
func StopPromptRuns(timeout time.Duration) int { return promptRuns.stop(timeout) }

func (b *runBroker) stop(timeout time.Duration) int {
	live := b.live()
	for _, stream := range live {
		stream.requestStop()
	}
	deadline := time.After(timeout)
	for _, stream := range live {
		select {
		case <-stream.finished:
		case <-deadline:
			unsettled := len(b.live())
			log.Warnf("%d prompt run(s) did not settle within %s of shutdown; they are reclaimed once this process exits", unsettled, timeout)
			return unsettled
		}
	}
	return 0
}

// live is the runs still in flight.
func (b *runBroker) live() []*runStream {
	b.mu.Lock()
	defer b.mu.Unlock()
	var live []*runStream
	for _, stream := range b.runs {
		if done, _, _ := stream.state(); !done {
			live = append(live, stream)
		}
	}
	return live
}
