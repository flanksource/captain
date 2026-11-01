package api

import (
	"fmt"
	"strings"
)

// Workflow declares the generate→verify loop around a run as hook
// declarations: an optional verification stage (commands run after each
// generation, whose failure feedback drives a re-run), and zero or more commit
// policies naming the lifecycle phase they fire at. It is the serializable form
// of pkg/ai/agent's Verify/Post hooks, and mirrors clicky-ui's
// AISpecRuntimeLocalWorkflow so the SpecRuntimeEditor "Verify" section
// round-trips.
//
// A spec with a Verify but an empty Prompt.User runs verify-only (generation is
// skipped); a spec with no Verify runs generate-only (today's behaviour).
type Workflow struct {
	Verify *Verify `json:"verify,omitempty" yaml:"verify,omitempty"`
	// Commits declares when and how the run's work is committed. Empty commits
	// nothing. Layers merge it by phase: a stanza only changes the fields it sets
	// on the inherited stanza with the same `on`.
	Commits []Commit `json:"commits,omitempty" yaml:"commits,omitempty" patchStrategy:"merge" patchMergeKey:"on"`
	// After declares shell steps run once at the end of the run, after the verify
	// verdict and the run's commits and before setup tears the worktree down. A
	// failing step fails the run. Maps to agent/verify.AfterHooksFor.
	After []AfterStep `json:"after,omitempty" yaml:"after,omitempty"`

	// AutoVerifyWithoutFixture is the explicit policy opt-in for hosts that
	// project a successful generate-only run into a durable verified state. A
	// false value keeps the durable work item open when no verification fixture
	// ran; success by itself is not treated as proof of correctness.
	AutoVerifyWithoutFixture bool `json:"autoVerifyWithoutFixture,omitempty" yaml:"autoVerifyWithoutFixture,omitempty"`
}

// Verify is the loop's definition-of-done: it runs after each generation and
// votes; a non-passing verdict appends feedback and triggers another iteration
// up to MaxIterations.
type Verify struct {
	// Commands are shell commands run as pass/fail checks (exit 0 = pass); their
	// output tail becomes the re-run feedback. Maps to agent/verify.CmdVerifier.
	// This is captain's runnable verify — the only part of Verify captain itself
	// executes.
	Commands []string `json:"commands,omitempty" yaml:"commands,omitempty"`
	// Fixture is a clicky-FixtureEditor markdown document (acceptance criteria /
	// LLM-judge checklist). Captain does not implement a fixture engine, but it
	// does dispatch this document: the declaration is handed to whatever claimed
	// the `fixture` verifier — a runner the host linked in-process, or the
	// external program named by `verify.fixtureRunner` in ~/.captain.yaml, which
	// receives the document on stdin and answers with a report.
	//
	// Declaring a fixture with neither registered is an error, not a pass: a
	// definition of done that contributes no check would let the run succeed
	// without ever being verified.
	Fixture string `json:"fixture,omitempty" yaml:"fixture,omitempty"`
	// Prompts are .prompt template paths run as LLM-judge checks: each renders
	// against the run's context and must yield {ok, reason, feedback}; a false
	// verdict's feedback drives the re-run like a failing command's output.
	// Maps to agent/verify.LLMJudgeVerifier.
	Prompts []string `json:"prompts,omitempty" yaml:"prompts,omitempty"`
	// Scope narrows verification to changed files vs the whole tree.
	Scope VerifyScope `json:"scope,omitempty" yaml:"scope,omitempty" jsonschema:"enum=,enum=all,enum=changed"`
	// MaxIterations caps the generate→verify loop; 0 means the run default (1).
	MaxIterations int `json:"maxIterations,omitempty" yaml:"maxIterations,omitempty" jsonschema:"minimum=0"`
}

// AfterStep is one post-run step, run when the run's outcome passes When and its
// `if` holds: Commands run sequentially with `sh -c` in Cwd, stopping at the
// first failure; Tool runs an inline tool with With as its stdin; Prompt runs a
// child .prompt with With as its variables.
type AfterStep struct {
	// When gates the step on the run's outcome: onVerify (default) runs only when
	// the run did not error and verification passed (or none was declared);
	// onSuccess runs when the run did not error; always runs regardless.
	When CommitWhen `json:"when,omitempty" yaml:"when,omitempty" jsonschema:"enum=onVerify,enum=onSuccess,enum=always"`
	// Cwd is where the commands or child prompt run: absolute, or relative to the
	// run's working directory (the worktree when setup relocated the run). Empty
	// is the run's.
	Cwd        string `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	HookAction `json:",inline" yaml:",inline"`
}

// EffectiveWhen resolves the default gate.
func (s AfterStep) EffectiveWhen() CommitWhen {
	if s.When == "" {
		return CommitWhenVerify
	}
	return s.When
}

// Validate refuses a gate it cannot honour and a step that runs nothing or
// several things.
func (s AfterStep) Validate() error {
	if !s.When.Valid() {
		return fmt.Errorf("when %q is invalid; want one of: onVerify, onSuccess, always", s.When)
	}
	if err := s.HookAction.Validate(HookEnvLifecycle); err != nil {
		return err
	}
	if s.Tool != "" && s.Cwd != "" {
		return fmt.Errorf("cwd is not read by a tool step: the tool runs in its own environment")
	}
	return nil
}

// Validate checks the workflow's enum-typed fields.
func (w *Workflow) Validate() error {
	if w == nil {
		return nil
	}
	for i, c := range w.Commits {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("workflow.commits[%d]: %w", i, err)
		}
	}
	for i, step := range w.After {
		if err := step.Validate(); err != nil {
			return fmt.Errorf("workflow.after[%d]: %w", i, err)
		}
	}
	if w.Verify == nil {
		return nil
	}
	if err := w.Verify.Scope.Validate(); err != nil {
		return err
	}
	if w.Verify.MaxIterations < 0 {
		return fmt.Errorf("workflow.verify.maxIterations must be >= 0")
	}
	for i, p := range w.Verify.Prompts {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("workflow.verify.prompts[%d] is empty", i)
		}
	}
	return nil
}
