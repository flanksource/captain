package api

import "fmt"

// Workflow declares the generate→verify loop around a run as hook
// declarations: an optional verification stage (commands run after each
// generation, whose failure feedback drives a re-run), and an optional postRun
// stage (commit the result). It is the serializable form of pkg/ai/agent's
// Verify/PostRun hooks, and mirrors clicky-ui's AISpecRuntimeLocalWorkflow so
// the SpecRuntimeEditor "Verify" section round-trips.
//
// A spec with a Verify but an empty Prompt.User runs verify-only (generation is
// skipped); a spec with no Verify runs generate-only (today's behaviour).
type Workflow struct {
	Verify  *Verify  `json:"verify,omitempty" yaml:"verify,omitempty"`
	PostRun *PostRun `json:"postRun,omitempty" yaml:"postRun,omitempty"`

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
	// LLM-judge checklist). Captain declares and reflects it in the spec schema
	// for the SpecRuntimeEditor, but does not execute it — only gavel runs
	// fixtures.
	Fixture string `json:"fixture,omitempty" yaml:"fixture,omitempty"`
	// Scope narrows verification to changed files vs the whole tree.
	Scope VerifyScope `json:"scope,omitempty" yaml:"scope,omitempty" jsonschema:"enum=,enum=all,enum=changed"`
	// MaxIterations caps the generate→verify loop; 0 means the run default (1).
	MaxIterations int `json:"maxIterations,omitempty" yaml:"maxIterations,omitempty" jsonschema:"minimum=0"`
}

// PostRun runs once after the loop ends cleanly (e.g. commit the agent's work).
type PostRun struct {
	Commit        bool   `json:"commit,omitempty" yaml:"commit,omitempty"`
	CommitMessage string `json:"commitMessage,omitempty" yaml:"commitMessage,omitempty"`
	DryRun        bool   `json:"dryRun,omitempty" yaml:"dryRun,omitempty"`
	KeepWorktree  bool   `json:"keepWorktree,omitempty" yaml:"keepWorktree,omitempty"`
}

// Validate checks the workflow's enum-typed fields.
func (w *Workflow) Validate() error {
	if w == nil || w.Verify == nil {
		return nil
	}
	if err := w.Verify.Scope.Validate(); err != nil {
		return err
	}
	if w.Verify.MaxIterations < 0 {
		return fmt.Errorf("workflow.verify.maxIterations must be >= 0")
	}
	return nil
}
