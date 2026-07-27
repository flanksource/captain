package api

import "fmt"

// CommitPhase names the lifecycle boundary a Commit is cut at. It mirrors
// pkg/ai/agent.Phase, which pkg/api cannot import (pkg/api is the leaf).
type CommitPhase string

const (
	// CommitOnTurn cuts a commit after every loop iteration, before that
	// iteration's verifiers vote — so a turn's work survives a failing verify,
	// a crashed provider, or an interrupt.
	CommitOnTurn CommitPhase = "turn"
	// CommitOnAgent cuts one commit when the generate→verify loop ends, with
	// verdicts known and an isolated worktree still live. This is the last
	// point at which the worktree's contents can be committed.
	CommitOnAgent CommitPhase = "agent"
	// CommitOnRun cuts one commit at the very end of the run. It still runs
	// before any worktree merge/teardown — commit hooks are registered ahead of
	// the worktree plugin precisely so there is something for the merge to take.
	// This is the default and matches captain's behaviour before phases existed.
	CommitOnRun CommitPhase = "run"
)

// AllCommitPhases lists the non-empty phases in lifecycle order.
func AllCommitPhases() []CommitPhase {
	return []CommitPhase{CommitOnTurn, CommitOnAgent, CommitOnRun}
}

// Valid reports whether p is a recognised phase (including the default "").
func (p CommitPhase) Valid() bool {
	switch p {
	case "", CommitOnTurn, CommitOnAgent, CommitOnRun:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown phase, naming the valid set.
func (p CommitPhase) Validate() error {
	if p.Valid() {
		return nil
	}
	return fmt.Errorf("invalid commit phase %q; want one of: turn, agent, run", p)
}

// CommitMode is how each commit is cut.
type CommitMode string

const (
	// CommitModeCommit cuts an ordinary commit every time.
	CommitModeCommit CommitMode = "commit"
	// CommitModeFixup cuts `git commit --fixup` against the run's anchor, so a
	// chain of them autosquashes back into one commit. The run's first commit
	// is always a real one regardless — a fixup needs something to fix up.
	CommitModeFixup CommitMode = "fixup"
	// CommitModeAmend folds each commit into the previous one, keeping exactly
	// one commit at all times without a rebase.
	CommitModeAmend CommitMode = "amend"
)

// AllCommitModes lists the non-empty commit modes.
func AllCommitModes() []CommitMode {
	return []CommitMode{CommitModeCommit, CommitModeFixup, CommitModeAmend}
}

// Valid reports whether m is a recognised mode (including the default "").
func (m CommitMode) Valid() bool {
	switch m {
	case "", CommitModeCommit, CommitModeFixup, CommitModeAmend:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown mode, naming the valid set.
func (m CommitMode) Validate() error {
	if m.Valid() {
		return nil
	}
	return fmt.Errorf("invalid commit mode %q; want one of: commit, fixup, amend", m)
}

// CommitWhen gates a commit on the run's outcome.
type CommitWhen string

const (
	// CommitWhenAlways commits regardless of outcome — the point of per-turn
	// commits, which exist precisely so failed runs leave their work behind.
	CommitWhenAlways CommitWhen = "always"
	// CommitWhenSuccess commits only when the run itself did not error.
	CommitWhenSuccess CommitWhen = "onSuccess"
	// CommitWhenVerify commits only when verification passed.
	CommitWhenVerify CommitWhen = "onVerify"
)

// AllCommitWhens lists the non-empty gates.
func AllCommitWhens() []CommitWhen {
	return []CommitWhen{CommitWhenAlways, CommitWhenSuccess, CommitWhenVerify}
}

// Valid reports whether w is a recognised gate (including the default "").
func (w CommitWhen) Valid() bool {
	switch w {
	case "", CommitWhenAlways, CommitWhenSuccess, CommitWhenVerify:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown gate, naming the valid set.
func (w CommitWhen) Validate() error {
	if w.Valid() {
		return nil
	}
	return fmt.Errorf("invalid commit when %q; want one of: always, onSuccess, onVerify", w)
}

// CommitStage selects which files a commit picks up.
type CommitStage string

const (
	// CommitStageWorktree stages everything in the working tree. Only safe when
	// the tree is an isolated worktree holding no work but the agent's.
	CommitStageWorktree CommitStage = "worktree"
	// CommitStageChanged stages only the files the agent is recorded as having
	// modified (Workspace.Changed). This is the only mode safe to use in a tree
	// that also holds the caller's own uncommitted work.
	CommitStageChanged CommitStage = "changed"
)

// AllCommitStages lists the non-empty staging modes.
func AllCommitStages() []CommitStage {
	return []CommitStage{CommitStageWorktree, CommitStageChanged}
}

// Valid reports whether s is a recognised staging mode (including the default "",
// which resolves by isolation — see pkg/ai/agent/commit).
func (s CommitStage) Valid() bool {
	switch s {
	case "", CommitStageWorktree, CommitStageChanged:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown staging mode, naming the valid set.
func (s CommitStage) Validate() error {
	if s.Valid() {
		return nil
	}
	return fmt.Errorf("invalid commit stage %q; want one of: worktree, changed", s)
}

// CommitGates is how much checking runs before each commit.
type CommitGates string

const (
	// CommitGatesNone runs plain git with no checks.
	CommitGatesNone CommitGates = "none"
	// CommitGatesCheap runs only checks fast enough for every turn: gitignore
	// and file-size, so secrets and build artifacts never enter the chain.
	CommitGatesCheap CommitGates = "cheap"
	// CommitGatesFull runs the host's complete pre-commit pipeline (hooks,
	// lint, dependency checks). Captain has no such pipeline of its own; this
	// requires a host-supplied commit callback and errors without one.
	CommitGatesFull CommitGates = "full"
)

// AllCommitGates lists the non-empty gate levels.
func AllCommitGates() []CommitGates {
	return []CommitGates{CommitGatesNone, CommitGatesCheap, CommitGatesFull}
}

// Valid reports whether g is a recognised gate level (including the default "",
// which means cheap).
func (g CommitGates) Valid() bool {
	switch g {
	case "", CommitGatesNone, CommitGatesCheap, CommitGatesFull:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown gate level, naming the valid set.
func (g CommitGates) Validate() error {
	if g.Valid() {
		return nil
	}
	return fmt.Errorf("invalid commit gates %q; want one of: none, cheap, full", g)
}

// Commit declares one commit policy: which lifecycle boundary commits are cut
// at, how they are cut, and what they pick up. A workflow may declare several.
//
// The common cases are one stanza each:
//
//	commits: [{on: turn}]                    # fixup chain per turn, squashed at the end
//	commits: [{on: run}]                     # one commit at the end
//	commits: [{on: agent, when: onVerify}]   # one commit if the loop verified, pre-merge
//	commits: [{on: turn, squash: false}]     # keep the chain for turn-by-turn review
type Commit struct {
	// On is the lifecycle boundary commits are cut at; defaults to run.
	On CommitPhase `json:"on,omitempty" yaml:"on,omitempty" jsonschema:"enum=turn,enum=agent,enum=run"`
	// Mode is how each commit is cut; defaults to fixup when On is turn (a
	// squashable chain is the only sane shape for many commits), else commit.
	Mode CommitMode `json:"mode,omitempty" yaml:"mode,omitempty" jsonschema:"enum=commit,enum=fixup,enum=amend"`
	// When gates commits on the run's outcome; defaults to always.
	When CommitWhen `json:"when,omitempty" yaml:"when,omitempty" jsonschema:"enum=always,enum=onSuccess,enum=onVerify"`
	// Message is the subject of the real commit (the anchor, in fixup mode).
	// Empty derives it from the prompt — never from the model, so a commit can
	// never fail or stall on an LLM call.
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
	// Anchor is the fixup target: empty means this run's first commit, "auto"
	// routes each file to the last commit that touched it, and anything else is
	// used as a git ref.
	Anchor string `json:"anchor,omitempty" yaml:"anchor,omitempty"`
	// Squash autosquashes the fixup chain once the run's commits are all cut.
	// Nil defaults to true in fixup mode — a chain of `fixup!` commits must not
	// escape into a branch someone pushes.
	Squash *bool `json:"squash,omitempty" yaml:"squash,omitempty"`
	// Base is the autosquash base ref, an escape hatch that is rarely needed:
	// empty rebases onto the anchor commit's parent, which spans exactly the
	// chain and nothing else. That also sidesteps the usual base probe
	// (origin/main, @{upstream}, …), none of which resolve on the freshly
	// created branch an isolated run commits to.
	Base string `json:"base,omitempty" yaml:"base,omitempty"`
	// Stage selects which files are committed; empty resolves by isolation
	// (worktree when the run is isolated, changed otherwise).
	Stage CommitStage `json:"stage,omitempty" yaml:"stage,omitempty" jsonschema:"enum=worktree,enum=changed"`
	// Gates is how much checking runs before each commit; defaults to cheap.
	Gates CommitGates `json:"gates,omitempty" yaml:"gates,omitempty" jsonschema:"enum=none,enum=cheap,enum=full"`
	// DryRun reports what would be committed without writing anything.
	DryRun bool `json:"dryRun,omitempty" yaml:"dryRun,omitempty"`
}

// Phase resolves On to its effective value.
func (c Commit) Phase() CommitPhase {
	if c.On == "" {
		return CommitOnRun
	}
	return c.On
}

// EffectiveMode resolves Mode to its effective value: a per-turn policy defaults
// to fixup so the many commits collapse back into one, anything else to a plain
// commit.
func (c Commit) EffectiveMode() CommitMode {
	if c.Mode != "" {
		return c.Mode
	}
	if c.Phase() == CommitOnTurn {
		return CommitModeFixup
	}
	return CommitModeCommit
}

// ShouldSquash reports whether the fixup chain is autosquashed once every commit
// is cut. Only meaningful in fixup mode, where it defaults to true.
func (c Commit) ShouldSquash() bool {
	if c.EffectiveMode() != CommitModeFixup {
		return false
	}
	if c.Squash == nil {
		return true
	}
	return *c.Squash
}

// EffectiveGates resolves Gates to its effective value.
func (c Commit) EffectiveGates() CommitGates {
	if c.Gates == "" {
		return CommitGatesCheap
	}
	return c.Gates
}

// Validate checks the commit policy's enum-typed fields and their combinations.
func (c Commit) Validate() error {
	for _, err := range []error{c.On.Validate(), c.Mode.Validate(), c.When.Validate(), c.Stage.Validate(), c.Gates.Validate()} {
		if err != nil {
			return err
		}
	}
	if c.Squash != nil && *c.Squash && c.EffectiveMode() != CommitModeFixup {
		return fmt.Errorf("commit squash requires mode fixup, got %q", c.EffectiveMode())
	}
	if c.Anchor != "" && c.EffectiveMode() != CommitModeFixup {
		return fmt.Errorf("commit anchor requires mode fixup, got %q", c.EffectiveMode())
	}
	// A per-turn commit is cut before that turn's verifiers vote, so the run's
	// outcome is not knowable yet and an outcome gate could only ever read the
	// zero value. Rejecting the combination beats honouring it as "always".
	if c.Phase() == CommitOnTurn && c.When != "" && c.When != CommitWhenAlways {
		return fmt.Errorf("commit when=%q is not available on=turn: a turn commits before its verifiers vote, so the outcome is not yet known; gate at on=agent instead", c.When)
	}
	return nil
}
