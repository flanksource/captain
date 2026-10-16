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
type AfterHook struct {
	Index int
	Step  api.AfterStep
	Opts  Options
}

// AfterHooksFor builds one AfterHook per workflow.after step, in declaration
// order. Nil when the workflow declares none.
func AfterHooksFor(wf *api.Workflow, opts Options) ([]any, error) {
	if wf == nil || len(wf.After) == 0 {
		return nil, nil
	}
	if err := wf.Validate(); err != nil {
		return nil, err
	}
	hooks := make([]any, 0, len(wf.After))
	for i, step := range wf.After {
		hooks = append(hooks, &AfterHook{Index: i, Step: step, Opts: opts})
	}
	return hooks, nil
}

func (h *AfterHook) Name() string { return fmt.Sprintf("after[%d]", h.Index) }

func (h *AfterHook) Phases() []agent.Phase { return []agent.Phase{agent.PhaseRun} }

func (h *AfterHook) Post(hc *agent.HookContext, _ agent.Phase) error {
	if reason := h.skipReason(hc); reason != "" {
		hc.Notify("[post-run] %s skipped: %s", h.Name(), reason)
		return nil
	}
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
