package api

import "fmt"

// Workflow declares the generate→verify loop around a run: an optional
// verification stage (commands / gavel scoring run after each generation, whose
// failure feedback drives a re-run) and an optional finalize stage (commit the
// result). It is the serializable form of pkg/ai/agent's VerifyPlugin /
// FinalizePlugin, and mirrors clicky-ui's AISpecRuntimeLocalWorkflow so the
// SpecRuntimeEditor "Verify" section round-trips.
//
// A spec with a Verify but an empty Prompt.User runs verify-only (generation is
// skipped); a spec with no Verify runs generate-only (today's behaviour).
type Workflow struct {
	Verify   *Verify   `json:"verify,omitempty" yaml:"verify,omitempty"`
	Finalize *Finalize `json:"finalize,omitempty" yaml:"finalize,omitempty"`
}

// Verify is the loop's definition-of-done: it runs after each generation and
// votes; a non-passing verdict appends feedback and triggers another iteration
// up to MaxIterations.
type Verify struct {
	// Commands are shell commands run as pass/fail checks (exit 0 = pass); their
	// output tail becomes the re-run feedback. Maps to agent/verify.CmdVerifier.
	Commands []string `json:"commands,omitempty" yaml:"commands,omitempty"`
	// Scope narrows verification to changed files vs the whole tree.
	Scope VerifyScope `json:"scope,omitempty" yaml:"scope,omitempty" jsonschema:"enum=,enum=all,enum=changed"`
	// MaxIterations caps the generate→verify loop; 0 means the run default (1).
	MaxIterations int `json:"maxIterations,omitempty" yaml:"maxIterations,omitempty" jsonschema:"minimum=0"`
	// Gavel enables gavel's acceptance-criteria / LLM-judge scoring as a verifier.
	Gavel bool `json:"gavel,omitempty" yaml:"gavel,omitempty"`
}

// Finalize runs once after the loop ends cleanly (e.g. commit the agent's work).
type Finalize struct {
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
