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
// the task list filters on, while Annotations are re-read on every snapshot and
// carry what only becomes true (or changes) while the process runs.
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
		Name:        agentTaskName(req, p.model),
		Kind:        "agent",
		Labels:      labels,
		Href:        req.HostLabel(labelHref),
		Background:  true,
		Annotations: p.taskAnnotations,
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

// taskAnnotations reports what has changed since the process started: the
// provider session only exists once the handshake lands, and whether a turn is
// in flight is the difference between an agent that is working and one that is
// waiting on its caller.
func (p *Provider) taskAnnotations() map[string]string {
	annotations := map[string]string{"state": "idle"}

	p.activeMu.Lock()
	active := p.active != nil
	p.activeMu.Unlock()
	if active {
		annotations["state"] = "running"
	}

	p.sessMu.Lock()
	sessionID := p.sessionID
	p.sessMu.Unlock()
	if sessionID != "" {
		annotations["session"] = sessionID
	}
	return annotations
}
