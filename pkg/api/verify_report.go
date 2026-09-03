package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// VerifyState is the terminal (or live) state of a verification report.
type VerifyState string

const (
	VerifyStateQueued    VerifyState = "queued"
	VerifyStateRunning   VerifyState = "running"
	VerifyStatePassed    VerifyState = "passed"
	VerifyStateFailed    VerifyState = "failed"
	VerifyStateErrored   VerifyState = "errored"
	VerifyStateWarned    VerifyState = "warned"
	VerifyStateSkipped   VerifyState = "skipped"
	VerifyStateCancelled VerifyState = "cancelled"
	VerifyStateTimedOut  VerifyState = "timed_out"
)

// AllVerifyStates lists every state in canonical order.
func AllVerifyStates() []VerifyState {
	return []VerifyState{
		VerifyStateQueued, VerifyStateRunning, VerifyStatePassed, VerifyStateFailed, VerifyStateErrored,
		VerifyStateWarned, VerifyStateSkipped, VerifyStateCancelled, VerifyStateTimedOut,
	}
}

// Validate rejects a state outside AllVerifyStates.
func (s VerifyState) Validate() error {
	for _, known := range AllVerifyStates() {
		if s == known {
			return nil
		}
	}
	return fmt.Errorf("invalid verify state %q", s)
}

// Report kinds produced by captain's own verifiers. A registry kind names the
// verifier family that produced a report; hosts add their own (e.g. "fixture").
const (
	VerifyKindCmd     = "cmd"
	VerifyKindPrompt  = "prompt"
	VerifyKindFunc    = "func"
	VerifyKindFixture = "fixture"
	// VerifyKindRound is what MergeReports stamps on a round whose reports do not
	// share one kind: the merged document is a whole verification round rather
	// than the output of any one verifier family.
	VerifyKindRound = "round"
)

// VerifySummary counts the leaves of a report's test tree. It is the wire twin
// of clicky-ui's TestSummary — a node carrying one is summarised from it rather
// than from its children (see VerifyNode.Summary) — and SummarizeNodes tallies
// leaves exactly as that package's countsFromLeaf does, timed-out bucket
// included.
type VerifySummary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Warned  int `json:"warned"`
	Skipped int `json:"skipped"`
	Pending int `json:"pending"`
	Running int `json:"running"`
	// TimedOut is its own bucket rather than a flavour of failed, and is spelled
	// `timedout` because that is the key clicky-ui's StatusCounts reads.
	TimedOut int `json:"timedout"`
}

// VerifyNodeProgress is a running node's in-flight position.
type VerifyNodeProgress struct {
	Phase string `json:"phase,omitempty"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// VerifyNodeContext mirrors clicky-ui's FixtureContext: what a command-shaped
// node ran and, for a CEL expectation, what it evaluated.
type VerifyNodeContext struct {
	Command       string         `json:"command,omitempty"`
	ExitCode      int            `json:"exit_code"`
	Cwd           string         `json:"cwd,omitempty"`
	CELExpression string         `json:"cel_expression,omitempty"`
	CELVars       map[string]any `json:"cel_vars,omitempty"`
	Expected      any            `json:"expected,omitempty"`
	Actual        any            `json:"actual,omitempty"`
}

// VerifyNode is one node of a verification tree. Its JSON is the snake_case
// wire shape of clicky-ui's Test so a TestRunner renders it unchanged; a node
// with Children is a group and carries no verdict of its own.
type VerifyNode struct {
	Name      string              `json:"name"`
	Framework string              `json:"framework,omitempty"`
	TaskID    string              `json:"task_id,omitempty"`
	File      string              `json:"file,omitempty"`
	Line      int                 `json:"line,omitempty"`
	Message   string              `json:"message,omitempty"`
	Command   string              `json:"command,omitempty"`
	WorkDir   string              `json:"work_dir,omitempty"`
	Stdout    string              `json:"stdout,omitempty"`
	Stderr    string              `json:"stderr,omitempty"`
	Duration  time.Duration       `json:"duration,omitempty"`
	Passed    bool                `json:"passed,omitempty"`
	Failed    bool                `json:"failed,omitempty"`
	Warned    bool                `json:"warned,omitempty"`
	Skipped   bool                `json:"skipped,omitempty"`
	Pending   bool                `json:"pending,omitempty"`
	Running   bool                `json:"running,omitempty"`
	TimedOut  bool                `json:"timed_out,omitempty"`
	Progress  *VerifyNodeProgress `json:"progress,omitempty"`
	Context   *VerifyNodeContext  `json:"context,omitempty"`
	Detail    json.RawMessage     `json:"detail,omitempty"`
	// Summary is this node's own tally, and it wins over its children — exactly
	// as clicky-ui's sum() reads `t.summary` before it recurses. It is what lets
	// a producer ship the counts of a suite whose rows it elided (a merged round
	// nests one group per report; a 40-test fixture may send its totals and only
	// the failures), where recursing would report that suite as empty.
	Summary  *VerifySummary `json:"summary,omitempty"`
	Children []VerifyNode   `json:"children,omitempty"`
}

// VerifyChecklistItem is one acceptance-criteria verdict. Passed is nil while
// the item has not been judged.
type VerifyChecklistItem struct {
	Item    string `json:"item"`
	Passed  *bool  `json:"passed"`
	Message string `json:"message,omitempty"`
}

// VerifyReport is a verifier's typed judgement: the verdict, the tree of what
// ran, and the acceptance-criteria checklist. It is what a Verify hook returns,
// what captain persists per iteration, and what the webapp renders.
type VerifyReport struct {
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	Ran      bool   `json:"ran"`
	Passed   bool   `json:"passed"`
	Reason   string `json:"reason,omitempty"`
	Feedback string `json:"feedback,omitempty"`
	// Iteration is the 1-based loop turn this report judged ("turn 1 of 3"), the
	// same numbering captain_prompt_run_iterations is keyed on. It is always on
	// the wire: with omitempty, an unstamped report and the first turn's report
	// arrived as the same document, and the store cannot tell them apart.
	Iteration  int                   `json:"iteration"`
	Summary    VerifySummary         `json:"summary"`
	Tests      []VerifyNode          `json:"tests,omitempty"`
	Checklist  []VerifyChecklistItem `json:"checklist,omitempty"`
	State      VerifyState           `json:"state"`
	StartedAt  *time.Time            `json:"started_at,omitempty"`
	FinishedAt *time.Time            `json:"finished_at,omitempty"`
	Duration   time.Duration         `json:"duration,omitempty"`
}

// SummarizeNodes counts the leaves of a tree; a node with children is a group,
// not a result. It mirrors clicky-ui's sum() exactly, so the counters captain
// persists and the ones the webapp recomputes never disagree:
//
//   - a node carrying its own Summary is counted from it and never recursed
//     into, so an elided child list still contributes the whole suite;
//   - a timed-out leaf counts only in TimedOut, never in Failed;
//   - a leaf carrying no status flag at all is not counted, Total included — it
//     is a placeholder row, not a queued test;
//   - otherwise the leaf's own flags are counted, one bucket each.
func SummarizeNodes(nodes []VerifyNode) VerifySummary {
	var s VerifySummary
	for i := range nodes {
		n := &nodes[i]
		switch {
		case n.Summary != nil:
			s = AddSummaries(s, *n.Summary)
		case len(n.Children) > 0:
			s = AddSummaries(s, SummarizeNodes(n.Children))
		default:
			s = AddSummaries(s, summarizeLeaf(n))
		}
	}
	return s
}

// summarizeLeaf is clicky-ui's countsFromLeaf: timed out wins outright, and a
// flagless leaf contributes nothing.
func summarizeLeaf(n *VerifyNode) VerifySummary {
	if n.TimedOut {
		return VerifySummary{Total: 1, TimedOut: 1}
	}
	var s VerifySummary
	if n.Passed {
		s.Passed = 1
	}
	if n.Failed {
		s.Failed = 1
	}
	if n.Warned {
		s.Warned = 1
	}
	if n.Skipped {
		s.Skipped = 1
	}
	if n.Pending {
		s.Pending = 1
	}
	if n.Running {
		s.Running = 1
	}
	if s != (VerifySummary{}) {
		s.Total = 1
	}
	return s
}

// AddSummaries totals two summaries, bucket by bucket, so a caller rolling
// several reports into one verdict never re-lists the fields (and never forgets
// the one added last).
func AddSummaries(into, from VerifySummary) VerifySummary {
	into.Total += from.Total
	into.Passed += from.Passed
	into.Failed += from.Failed
	into.Warned += from.Warned
	into.Skipped += from.Skipped
	into.Pending += from.Pending
	into.Running += from.Running
	into.TimedOut += from.TimedOut
	return into
}

// StateForNode derives a state from one node — the one-node case of
// StateForReport, and defined as it rather than as a second hand-ordered switch.
// Two switches drifted on the leaves that carry contradictory flags (a node both
// skipped and running, both pending and skipped): NewNodeReport stamped the
// state one of them chose and Validate then rejected the report against the
// other. There is one precedence, and it lives in StateForReport.
func StateForNode(n VerifyNode) VerifyState {
	return StateForReport([]VerifyNode{n})
}

// StateForReport derives a report's state from its whole tree, in the order a
// reader cares about: an outright failure first, then a check that never
// finished, then a soft failure, then anything still moving, then the states a
// finished-and-uneventful tree can be in. An empty tree has not started, so it
// is queued.
//
// It is the one definition of that precedence: StateForNode is this function
// applied to a single node, so the state NewNodeReport stamps and the one
// Validate checks can never disagree.
func StateForReport(tests []VerifyNode) VerifyState {
	s := SummarizeNodes(tests)
	switch {
	case s.Failed > 0:
		return VerifyStateFailed
	case s.TimedOut > 0:
		return VerifyStateTimedOut
	case s.Warned > 0:
		return VerifyStateWarned
	case s.Running > 0:
		return VerifyStateRunning
	case s.Pending > 0:
		return VerifyStateQueued
	case s.Skipped > 0:
		return VerifyStateSkipped
	case s.Passed > 0:
		return VerifyStatePassed
	default:
		return VerifyStateQueued
	}
}

// NewNodeReport builds a report from a single leaf: the verdict is the leaf's
// state, and only a passed leaf passes.
func NewNodeReport(kind, name string, node VerifyNode) VerifyReport {
	state := StateForNode(node)
	return VerifyReport{
		Kind:     kind,
		Name:     name,
		Ran:      state != VerifyStateQueued,
		Passed:   state == VerifyStatePassed,
		Reason:   node.Message,
		Summary:  SummarizeNodes([]VerifyNode{node}),
		Tests:    []VerifyNode{node},
		State:    state,
		Duration: node.Duration,
	}
}

// HostStamped reports whether the state is one the producing host asserts
// rather than one the tree implies. A runner that could not schedule its nodes
// (errored) or a run stopped mid-check (cancelled) leaves queued leaves behind
// that say nothing about why; no node flag maps to either, so Validate takes
// them as stamped. Neither can pass.
func (s VerifyState) HostStamped() bool {
	return s == VerifyStateErrored || s == VerifyStateCancelled
}

// Validate checks the report's internal consistency: only a passed report
// passes, a report that never ran is queued, running, errored or cancelled,
// the summary matches the leaves of its tree, and — unless the host stamped a
// terminal state of its own — the state is the one that tree justifies.
//
// The state check is what stops a red run reading as green downstream: the
// webapp colours its badge from State while the panel lists the tree, and a CEL
// predicate written against the state would pass on a report whose tests failed.
//
// A live snapshot is a valid report: a ProgressVerifier publishes one that is
// already running before it has run, so `running` joins `queued` as a state a
// not-yet-finished report is allowed to be in.
func (r VerifyReport) Validate() error {
	if r.Kind == "" {
		return fmt.Errorf("verify report: kind is required")
	}
	if err := r.State.Validate(); err != nil {
		return fmt.Errorf("verify report %q: %w", r.Name, err)
	}
	if r.Passed && r.State != VerifyStatePassed {
		return fmt.Errorf("verify report %q: passed=true with state %q", r.Name, r.State)
	}
	if want := SummarizeNodes(r.Tests); want != r.Summary {
		return fmt.Errorf("verify report %q: summary %+v does not match its %d leaf node(s) %+v", r.Name, r.Summary, want.Total, want)
	}
	if r.State.HostStamped() {
		return nil
	}
	if !r.Ran && r.State != VerifyStateQueued && r.State != VerifyStateRunning {
		return fmt.Errorf("verify report %q: ran=false with state %q; a report that did not run is queued, running, errored or cancelled", r.Name, r.State)
	}
	if len(r.Tests) > 0 {
		if want := StateForReport(r.Tests); want != r.State {
			return fmt.Errorf("verify report %q: state %q but its tests are %q", r.Name, r.State, want)
		}
	}
	return nil
}

// CELVars renders the report as the plain map a host binds to the CEL variable
// `verify` (wire field names, numbers as float64) so predicates such as
// `verify.summary.failed > 0` and `verify.checklist.all(i, i.passed)` read the
// same shape the webapp does.
//
// A report a verifier built with an unencodable detail is an error rather than
// a panic: the caller is a host evaluating a predicate, and it can report a
// broken report far better than a stack unwinding through its evaluator can.
func (r VerifyReport) CELVars() (map[string]any, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("verify report %q: marshal: %w", r.Name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("verify report %q: unmarshal: %w", r.Name, err)
	}
	if out["checklist"] == nil {
		out["checklist"] = []any{}
	}
	if out["tests"] == nil {
		out["tests"] = []any{}
	}
	return out, nil
}
