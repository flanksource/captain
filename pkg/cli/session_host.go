package cli

import "context"

// StartSessionHost runs the process-wide upkeep behind SessionHandler and the
// prompt-run handlers: finished prompt runs are pruned until ctx ends. Every
// process that serves them (captain serve, gavel) starts it once and calls the
// returned stop on shutdown, which ends the pruner and stops every live chat
// run so no agent outlives the host.
func StartSessionHost(ctx context.Context) (stop func()) {
	return startSessionHost(ctx, promptRuns, promptChats).stop
}

type sessionHost struct {
	cancel context.CancelFunc
	pruned chan struct{}
	chats  *chatBroker
}

func startSessionHost(ctx context.Context, runs *runBroker, chats *chatBroker) *sessionHost {
	ctx, cancel := context.WithCancel(ctx)
	host := &sessionHost{cancel: cancel, pruned: make(chan struct{}), chats: chats}
	go func() {
		defer close(host.pruned)
		prunePromptRuns(ctx, runs)
	}()
	return host
}

func (h *sessionHost) stop() {
	h.cancel()
	<-h.pruned
	h.chats.stopAll()
}
