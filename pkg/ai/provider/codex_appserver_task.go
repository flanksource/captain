package provider

import (
	"fmt"
	"strconv"

	"github.com/flanksource/clicky/exec"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// taskIdentity describes the supervised app-server to whoever is watching the
// task list, so concurrent agents are told apart by the work they are doing
// rather than by the identical binary path clicky would otherwise name them by.
//
// Unlike the claude-agent provider the app-server is spawned once and reused
// across turns, so the fixed Labels can only describe the run that started it.
// Anything that moves from turn to turn belongs in Metadata, which is re-read on
// every snapshot.
func (c *CodexAppServer) taskIdentity() exec.SupervisedTaskOptions {
	c.mu.Lock()
	runLabels := c.runLabels
	c.mu.Unlock()

	labels := map[string]string{}
	for key, value := range runLabels {
		if value != "" {
			labels[key] = value
		}
	}
	runtime := api.RuntimeOf(api.OpenAI, api.ModeAgent)
	labels["model"] = c.model
	labels["provider"] = runtime.Provider
	labels["mode"] = string(runtime.Mode)

	name := runLabels["title"]
	if name == "" {
		name = fmt.Sprintf("codex app-server (%s)", c.model)
	}
	return exec.SupervisedTaskOptions{
		Name:   name,
		Kind:   "agent",
		Labels: labels,
		Href:   runLabels["href"],
		// The app-server outlives any wait its caller makes, so it must not be
		// counted by a global task drain — see the claude-agent provider for the
		// deadlock this avoids.
		Background: true,
		Metadata:   c.taskMetadata,
	}
}

// taskMetadata reports what changes while the server runs: whether a turn is in
// flight, the thread the current run is working in, and the run itself — the
// app-server outlives any one of them, so none can be a fixed Label.
func (c *CodexAppServer) taskMetadata() any {
	c.mu.Lock()
	defer c.mu.Unlock()

	metadata := ai.AgentMetadata{
		State:  ai.AgentIdle,
		Thread: c.threadID,
		Run:    c.runLabels["title"],
	}
	if c.active != nil {
		metadata.State = ai.AgentRunning
	}
	// Turn is left nil: the app-server's turn state carries no plan mode, prompt
	// count or interrupt flag to report, and State already says one is in flight.
	return metadata
}

// rememberRunLabels records the host's identification of the current run so the
// supervised process can name itself. Recorded per turn for the same reason the
// approval posture is: the spawn path cannot reach the request.
func (c *CodexAppServer) rememberRunLabels(req ai.Request) {
	labels := req.HostLabels()
	if budget := req.Budget.Cost; budget > 0 {
		labels["budget"] = strconv.FormatFloat(budget, 'f', -1, 64)
	}
	c.mu.Lock()
	c.runLabels = labels
	c.mu.Unlock()
}
