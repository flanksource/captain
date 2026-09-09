package ai

// AgentMetadata is the structured account a supervised agent process reports on
// every task snapshot — what clicky carries in ProcessDetails.Metadata and a
// task view renders in the row header.
//
// The split from task Labels is deliberate and unchanged: Labels are fixed when
// the run starts and are what a task list filters on (model, provider, mode,
// the host's own run labels), while this carries only what becomes true, or
// changes, while the process runs. Being a struct rather than a string map is
// what lets the in-flight turn be reported as a nested object instead of a
// handful of flattened, separately-parsed keys.
type AgentMetadata struct {
	State AgentState `json:"state"`
	// Session is the provider's own session id, which only exists once the
	// handshake has landed.
	Session string `json:"session,omitempty"`
	// Thread is the conversation the current run is working in, where the
	// provider models one (the codex app-server does; the claude-agent SDK
	// session does not).
	Thread string `json:"thread,omitempty"`
	// Run names the work the host asked for, when it gave the run a title.
	Run string `json:"run,omitempty"`
	// Turn describes the turn in flight, and is nil when the agent is idle.
	Turn *AgentTurn `json:"turn,omitempty"`
}

// AgentState is the difference between an agent that is working and one that is
// waiting on its caller — the single most useful thing to see in a task row.
type AgentState string

const (
	AgentIdle    AgentState = "idle"
	AgentRunning AgentState = "running"
)

// AgentTurn is the in-flight turn's shape.
type AgentTurn struct {
	// PlanMode marks a plan-only turn, which ends on ExitPlanMode rather than on
	// the usual terminal signal.
	PlanMode bool `json:"planMode,omitempty"`
	// Pending is how many prompts the turn is still waiting on answers for.
	Pending int `json:"pending,omitempty"`
	// Interrupting is set once an interrupt has been requested but the turn has
	// not yet wound down.
	Interrupting bool `json:"interrupting,omitempty"`
}
