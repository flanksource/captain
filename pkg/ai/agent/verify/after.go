package verify

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// afterErrorTail bounds the command output quoted in a failing after step's
// error: it is logged and stored as the run's error, not fed to a model.
const afterErrorTail = 8 << 10

// AfterHook runs one workflow.after step once, at PhaseRun: after the verify
// verdict and the run's commits are settled, and — placed before setup in the
// hook list — while a relocated worktree is still live. Each command runs with
// `sh -c` through the same bounded process runner as a verify command, reports
// on the run's stream under the verify verdict kinds, and the first failure
// stops the step and fails the run.
//
// A tool or prompt step runs through Opts.Actions instead, with its `with`
// rendered over the lifecycle variables. Its output is recorded for later steps
// as steps[Index]; a skipped or commands step records null.
type AfterHook struct {
	Index int
	Step  api.AfterStep
	Opts  Options
	steps *[]any
}

// AfterHooksFor builds one AfterHook per workflow.after step, in declaration
// order, sharing one steps record. Nil when the workflow declares none.
func AfterHooksFor(wf *api.Workflow, opts Options) ([]any, error) {
	if wf == nil || len(wf.After) == 0 {
		return nil, nil
	}
	if err := wf.Validate(); err != nil {
		return nil, err
	}
	steps := make([]any, 0, len(wf.After))
	hooks := make([]any, 0, len(wf.After))
	for i, step := range wf.After {
		if len(step.Commands) == 0 && opts.Actions == nil {
			kind, _ := step.Kind()
			return nil, fmt.Errorf("workflow.after[%d] runs a %s, but the run supplies no hook action runner", i, kind)
		}
		hooks = append(hooks, &AfterHook{Index: i, Step: step, Opts: opts, steps: &steps})
	}
	return hooks, nil
}

func (h *AfterHook) Name() string { return fmt.Sprintf("after[%d]", h.Index) }

func (h *AfterHook) Phases() []agent.Phase { return []agent.Phase{agent.PhaseRun} }

func (h *AfterHook) Post(hc *agent.HookContext, _ agent.Phase) error {
	output, err := h.post(hc)
	if h.steps != nil {
		*h.steps = append(*h.steps, output)
	}
	return err
}

func (h *AfterHook) post(hc *agent.HookContext) (any, error) {
	if reason := h.skipReason(hc); reason != "" {
		hc.Notify("[post-run] %s skipped: %s", h.Name(), reason)
		return nil, nil
	}
	vars, err := h.variables(hc)
	if err != nil {
		return nil, err
	}
	run, err := h.Step.EvalIf(api.HookEnvLifecycle, vars)
	if err != nil {
		return nil, fmt.Errorf("workflow.after[%d]: %w", h.Index, err)
	}
	if !run {
		hc.Notify("[post-run] %s skipped: if %s is false", h.Name(), h.Step.If)
		return nil, nil
	}
	if len(h.Step.Commands) == 0 {
		return h.runAction(hc, vars)
	}
	return nil, h.runCommands(hc)
}

func (h *AfterHook) runCommands(hc *agent.HookContext) error {
	dir, err := h.dir(hc)
	if err != nil {
		return err
	}
	for _, cmd := range h.Step.Commands {
		if err := h.run(hc, dir, strings.TrimSpace(cmd)); err != nil {
			return err
		}
	}
	return nil
}

// skipReason applies the step's outcome gate; empty means run. A Post hook
// error earlier in the run blocks every gated step, because the runner does not
// short-circuit and a refused commit must not be followed by a landing step.
func (h *AfterHook) skipReason(hc *agent.HookContext) string {
	when := h.Step.EffectiveWhen()
	switch {
	case when == api.CommitWhenAlways:
		return ""
	case hc.Failed:
		return "the run failed"
	case hc.PostErr != nil:
		return "an earlier post-run hook failed"
	case when == api.CommitWhenVerify && !hc.Verified:
		return "verification did not pass"
	}
	return ""
}

func (h *AfterHook) dir(hc *agent.HookContext) (string, error) {
	cwd, runCwd := strings.TrimSpace(h.Step.Cwd), hc.Workspace().Cwd
	switch {
	case cwd == "":
		return runCwd, nil
	case filepath.IsAbs(cwd):
		return filepath.Clean(cwd), nil
	case runCwd == "":
		return "", fmt.Errorf("workflow.after[%d].cwd %q is relative but the run has no working directory", h.Index, cwd)
	}
	return filepath.Join(runCwd, cwd), nil
}

// run executes one command and records its verdict on the run's stream exactly
// as a verify command's is recorded, so a renderer shows both the same way.
func (h *AfterHook) run(hc *agent.HookContext, dir, cmd string) error {
	plugin := New("after:"+cmd, &CmdVerifier{
		Cmd: "sh", Args: []string{"-c", cmd}, FeedbackTail: afterErrorTail,
		Env: h.Opts.Env, Wrap: h.Opts.Wrap, Timeout: h.Opts.Timeout, Output: h.Opts.Output,
	})
	started := time.Now()
	vd, err := plugin.v.Verify(hc, dir, nil)
	if err != nil {
		return fmt.Errorf("workflow.after[%d] %q: %w", h.Index, cmd, err)
	}
	elapsed := time.Since(started)
	report, err := plugin.report(vd, hc.Iteration+1, elapsed)
	if err != nil {
		return err
	}
	vd.Report = report
	plugin.notify(hc, vd, elapsed)
	if !vd.OK {
		return fmt.Errorf("workflow.after[%d] %q failed in %s: %s\n%s", h.Index, cmd, dir, vd.Reason, vd.Feedback)
	}
	return nil
}
