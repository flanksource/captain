package claudeagent

import (
	"fmt"
	"strconv"

	"github.com/flanksource/clicky/exec"

	"github.com/flanksource/captain/pkg/ai"
)

// Reserved label keys the host may set to name its run. Everything else in
// Spec.Labels is passed through untouched, except the runtime-owned keys
// api.RuntimeOwnedLabels reserves for the provider.
const (
	labelTitle = "title"
	labelHref  = "href"
)

// taskIdentity describes the supervised agent process to whoever is watching
// the task list. Without it clicky falls back to argv[0], so every concurrent
// agent renders as the same anonymous `.../node_modules/.bin/tsx` row and an
// operator cannot tell which run is which.
//
// The split is deliberate: Labels are fixed when the run starts and are what
// the task list filters on, while Metadata is re-read on every snapshot and
// carries what only becomes true (or changes) while the process runs.
func (p *Provider) taskIdentity(req ai.Request) exec.SupervisedTaskOptions {
	labels := req.HostLabels()
	runtime := p.GetRuntime()
	labels["model"] = p.model
	labels["provider"] = runtime.Provider
	labels["mode"] = string(runtime.Mode)
	if req.SessionID != "" {
		labels["runSession"] = req.SessionID
	}
	if budget := req.Budget.Cost; budget > 0 {
		labels["budget"] = strconv.FormatFloat(budget, 'f', -1, 64)
	}

	return exec.SupervisedTaskOptions{
		Name:       agentTaskName(req, p.model),
		Kind:       "agent",
		Labels:     labels,
		Href:       req.HostLabel(labelHref),
		Background: true,
		Metadata:   p.taskMetadata,
	}
}

// agentTaskName prefers the host's own name for the work, because "implement
// the stack-trace viewer" identifies a run and "claude-agent" does not.
func agentTaskName(req ai.Request, model string) string {
	if title := req.HostLabel(labelTitle); title != "" {
		return title
	}
	return fmt.Sprintf("claude-agent (%s)", model)
}

// taskMetadata reports what has changed since the process started: the provider
// session only exists once the handshake lands, and whether a turn is in flight
// is the difference between an agent that is working and one that is waiting on
// its caller. The turn reports its own shape, so a viewer can tell a plan-only
// turn, or one already winding down under an interrupt, from an ordinary one.
func (p *Provider) taskMetadata() any {
	metadata := ai.AgentMetadata{State: ai.AgentIdle}

	p.activeMu.Lock()
	active := p.active
	p.activeMu.Unlock()
	if active != nil {
		metadata.State = ai.AgentRunning
		metadata.Turn = active.summary()
	}

	p.sessMu.Lock()
	metadata.Session = p.sessionID
	p.sessMu.Unlock()
	return metadata
}
