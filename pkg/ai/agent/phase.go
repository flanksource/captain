package agent

import (
	"fmt"
	"strings"
)

// Phase names a post-boundary in a run's lifecycle. A Post hook declares which
// phases it handles and the runner dispatches only those:
//
//	PreRun → loop{ generate → [turn] → verify } → [agent] → merge/cleanup → [run] → Output
//
// The three are distinct in what is settled by the time they fire, which is what
// makes them useful to a committing hook: at PhaseTurn the turn's edits are on
// disk but its verdict is not in yet; at PhaseAgent every verdict is in and the
// worktree is still live; at PhaseRun the worktree may already be merged away.
type Phase string

const (
	// PhaseTurn fires after each loop iteration, before that iteration's
	// verifiers vote — so a turn's work can be made durable even when
	// verification then fails.
	PhaseTurn Phase = "turn"
	// PhaseAgent fires once after the generate→verify loop ends, with verdicts
	// known and the worktree still live. It is the last point at which a hook
	// can act on the isolated tree.
	PhaseAgent Phase = "agent"
	// PhaseRun fires once at the very end, after merge/teardown.
	PhaseRun Phase = "run"
)

// AllPhases lists every phase in lifecycle order.
func AllPhases() []Phase { return []Phase{PhaseTurn, PhaseAgent, PhaseRun} }

// Valid reports whether p is one of the supported phases.
func (p Phase) Valid() bool {
	for _, x := range AllPhases() {
		if p == x {
			return true
		}
	}
	return false
}

// PhaseList renders the supported phases as a comma-separated string.
func PhaseList() string {
	parts := make([]string, len(AllPhases()))
	for i, p := range AllPhases() {
		parts[i] = string(p)
	}
	return strings.Join(parts, ", ")
}

// ParsePhase resolves a spec/flag value into a Phase, defaulting empty to
// PhaseRun — the boundary captain committed at before phases existed.
func ParsePhase(s string) (Phase, error) {
	if s == "" {
		return PhaseRun, nil
	}
	if p := Phase(s); p.Valid() {
		return p, nil
	}
	return "", fmt.Errorf("invalid phase %q (valid: %s)", s, PhaseList())
}
