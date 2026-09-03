// Package verify provides Verify hooks for agent.Runner: a generic command
// verifier, a function verifier, and the plumbing to scope a verifier to the
// files an agent changed. Gavel layers richer lint/test/checklist verifiers on
// top via its own fixture engine.
package verify

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// Verdict is a Verifier's judgement. Feedback is fed back into the next
// iteration's prompt when OK is false. Report is the typed form of the same
// verdict — the tree of what ran and the checklist — for persistence and
// rendering; a Verifier that leaves it nil gets a one-node report synthesised
// from OK/Reason/Feedback by the Plugin.
type Verdict struct {
	OK       bool
	Reason   string
	Feedback string
	Report   *api.VerifyReport
}

// Verifier checks the state of a working tree after an agent turn. cwd is where
// the run executed; changed is the agent's changed files when the run scope is
// ScopeChanged, and nil otherwise (meaning "verify the whole tree").
type Verifier interface {
	Verify(ctx context.Context, cwd string, changed []string) (Verdict, error)
}

// Plugin adapts a Verifier into an agent.Verify hook: it scopes the verifier,
// and on failure returns the exact next request (feedback appended to the prompt)
// as the loop's Retry.
type Plugin struct {
	name string
	v    Verifier
	// progress are the extra sinks a caller registered for in-flight snapshots,
	// alongside the run's own event stream. See OnProgress.
	progress []func(api.VerifyReport)
}

// New wraps a Verifier as a named agent.Verify hook.
func New(name string, v Verifier) *Plugin { return &Plugin{name: name, v: v} }

// OnProgress adds a sink for the in-flight snapshots a ProgressVerifier
// reports, on top of the run's event stream. HooksFor calls it with
// Options.Progress so a factory cannot forget to wire the caller's sink; a nil
// sink registers nothing.
func (p *Plugin) OnProgress(sink func(api.VerifyReport)) *Plugin {
	if sink != nil {
		p.progress = append(p.progress, sink)
	}
	return p
}

func (p *Plugin) Name() string { return p.name }

// Verifier exposes the wrapped Verifier so a caller that runs checks outside
// an agent loop — the git-agent receive path — can drive it directly with its
// own cwd and changed set instead of an agent.HookContext.
func (p *Plugin) Verifier() Verifier { return p.v }

func (p *Plugin) Verify(hc *agent.HookContext) (agent.VerifyResult, error) {
	ws := hc.Workspace()
	var changed []string
	if hc.Scope == agent.ScopeChanged {
		changed = ws.Changed
	}
	emitter := p.watchProgress(hc)
	started := time.Now()
	vd, err := p.v.Verify(hc, ws.Cwd, changed)
	if err != nil {
		// No notice: Notify is purely informational, and a hook that failed
		// reports by returning an error. The abort is the report — including the
		// snapshot the window was holding, which is dropped with it.
		return agent.VerifyResult{}, err
	}
	// Before the verdict, so a reader sees the last thing that ran and then how
	// it ended, in that order.
	emitter.flush()
	elapsed := time.Since(started)
	// HookContext.Iteration is the loop's 0-based index; a report and a verdict
	// name the turn the way a person and the iteration store do, from 1.
	iteration := hc.Iteration + 1
	report, err := p.report(vd, iteration, elapsed)
	if err != nil {
		return agent.VerifyResult{}, err
	}
	vd.Report = report
	p.notify(hc, vd, elapsed)
	result := agent.VerifyResult{Valid: vd.OK, Report: vd.Report, Iteration: iteration}
	if !vd.OK {
		result.Retry = retryWithFeedback(hc.Request, vd.Feedback)
	}
	return result, nil
}

// watchProgress hands a ProgressVerifier a coalescing sink that reports onto
// the run's stream and into whatever sinks the caller registered, and returns
// the emitter so the caller can flush the held snapshot before the verdict. A
// verifier that reports no progress gets an emitter nobody ever feeds.
func (p *Plugin) watchProgress(hc *agent.HookContext) *progressEmitter {
	sinks := append([]func(api.VerifyReport){p.notifyProgress(hc)}, p.progress...)
	emitter := newProgressEmitter(ProgressInterval, sinks...)
	if pv, ok := p.v.(ProgressVerifier); ok {
		pv.SetProgress(emitter.publish)
	}
	return emitter
}

// notifyProgress reports one snapshot on the run's stream under its own kind,
// with the typed report on Raw exactly as the verdict carries it — a renderer
// that draws the verification tree redraws it from the same shape while the
// check is still running.
//
// It Emits rather than Notifies: a snapshot is true only while it is on screen,
// and recording each one as a workspace notice wrote every superseded count into
// the persisted transcript and buried the verdict underneath them. There is
// deliberately no text — the event carries the report a renderer draws, and a
// sentence describing it would be the thing that got recorded.
func (p *Plugin) notifyProgress(hc *agent.HookContext) func(api.VerifyReport) {
	return func(report api.VerifyReport) {
		if report.Name == "" {
			report.Name = p.name
		}
		hc.Emit(ai.Event{Kind: ai.EventVerifyProgress, Tool: p.name, Raw: &report})
	}
}

// report completes the verifier's typed report: a Verifier that returned none
// gets a one-node report synthesised from its verdict, and every report carries
// the hook's name, the verdict's reason/feedback, the iteration it judged and
// the wall clock it took, so a consumer never has to reach back into the
// Verdict for those.
//
// A report that disagrees with the verdict it came with is an error. The two are
// the same judgement written twice, and quietly preferring one of them is how a
// check that failed reaches the store, the webapp and the next turn as a pass.
func (p *Plugin) report(vd Verdict, iteration int, elapsed time.Duration) (*api.VerifyReport, error) {
	report := vd.Report
	if report == nil {
		synthesised := api.NewNodeReport(api.VerifyKindFunc, p.name, api.VerifyNode{
			Name: p.name, Passed: vd.OK, Failed: !vd.OK, Message: vd.Reason, Duration: elapsed,
		})
		report = &synthesised
	}
	if report.Passed != vd.OK {
		return nil, fmt.Errorf("verify %q: report %q reports passed=%t but its verdict says OK=%t",
			p.name, report.Name, report.Passed, vd.OK)
	}
	if report.Name == "" {
		report.Name = p.name
	}
	if report.Reason == "" {
		report.Reason = vd.Reason
	}
	if report.Feedback == "" {
		report.Feedback = vd.Feedback
	}
	if report.Duration == 0 {
		report.Duration = elapsed
	}
	report.Iteration = iteration
	return report, nil
}

// notify records the verdict on the run's stream and workspace, the way every
// other lifecycle hook records what it did.
//
// Verification is the loop's definition of done and was the only participant
// that left no trace. A failing verdict's output travels into the next turn's
// prompt and nowhere else, so it is lost entirely on the last iteration — the
// one that decides the run — and a passing verdict was never recorded at all. A
// reader was left with a turn, a long pause, and another turn, with nothing
// saying what the check had reported or how long it took.
//
// It reports under EventVerified / EventVerifyFailed rather than as a generic
// system line, so a consumer selects on the verdict instead of parsing it, and
// carries the check's name and wall clock as fields rather than only in prose.
//
// The feedback is included rather than summarized: it is the verdict's whole
// content, it is already tail-bounded by the verifier, and on the final
// iteration this is the only place it survives.
//
// The verdict leads and the hook's name follows it, because a row that has to
// fit one line shows the front of the text: a workflow hook is named after the
// whole shell command it runs, so naming it first spends the whole line before
// saying whether anything passed.
//
// The typed report (vd.Report, when the verdict carries one) rides on Raw so a
// renderer that wants the tree — the webapp's verification panel, a transcript
// rehydrating a stored run — reads it from the same event the prose came from.
func (p *Plugin) notify(hc *agent.HookContext, vd Verdict, elapsed time.Duration) {
	event := ai.Event{Kind: ai.EventVerifyFailed, Tool: p.name, Duration: elapsed}
	if vd.Report != nil {
		event.Raw = vd.Report
	}
	if vd.OK {
		event.Kind, event.Success = ai.EventVerified, true
		event.Text = fmt.Sprintf("passed in %s — %s", took(elapsed), p.name)
		hc.NotifyEvent(event)
		return
	}
	reason := strings.TrimSpace(vd.Reason)
	if reason == "" {
		reason = "no reason reported"
	}
	event.Reason = reason
	event.Text = fmt.Sprintf("failed in %s: %s — %s", took(elapsed), reason, p.name)
	if feedback := strings.TrimSpace(vd.Feedback); feedback != "" {
		event.Text += "\n" + feedback
	}
	hc.NotifyEvent(event)
}

// took renders a verify's wall clock at a precision that stays useful across
// both scales it runs at: milliseconds for a lint that finishes instantly,
// whole seconds for a test suite where the fractions are noise.
func took(d time.Duration) time.Duration {
	if d >= time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Millisecond)
}

// retryWithFeedback builds the next request: the current one with the verifier's
// feedback appended to the user prompt.
func retryWithFeedback(req *ai.Request, feedback string) *ai.Request {
	next := *req
	next.Prompt.User = strings.TrimRight(req.Prompt.User, "\n") +
		"\n\n[verifier feedback]\n" + feedback + "\n\nFix the issues above and continue."
	return &next
}

// FuncVerifier adapts a function to the Verifier interface.
type FuncVerifier func(ctx context.Context, cwd string, changed []string) (Verdict, error)

func (f FuncVerifier) Verify(ctx context.Context, cwd string, changed []string) (Verdict, error) {
	return f(ctx, cwd, changed)
}

// CmdVerifier runs an external command (lint, test, build, …) in the run's cwd.
// A zero exit code is a pass; a non-zero exit is a failure whose feedback is the
// tail of the combined output. When PerFile is set the changed files are
// appended to the command's arguments.
//
// The command is bounded three ways: the caller's context and Timeout cap its
// wall clock, it runs in its own process group so a kill reaches its children,
// and its output is tail-bounded as it streams rather than buffered in full
// (see exec.go, which ExternalVerifier shares).
type CmdVerifier struct {
	Cmd          string
	Args         []string
	PerFile      bool
	FeedbackTail int             // max bytes of output fed back; 0 ⇒ defaultFeedbackTail
	Timeout      time.Duration   // wall-clock bound; 0 ⇒ DefaultCmdTimeout
	Env          []string        // command environment; nil ⇒ inherit the process's
	Wrap         CommandWrapFunc // optional confinement seam; see CommandWrapFunc
}

func (c *CmdVerifier) Verify(ctx context.Context, cwd string, changed []string) (Verdict, error) {
	args := append([]string(nil), c.Args...)
	if c.PerFile {
		args = append(args, changed...)
	}
	output := newTailBuffer(c.FeedbackTail)
	outcome, err := runProcess(ctx, execRequest{
		Cmd: c.Cmd, Args: args, Dir: cwd, Env: c.Env, Wrap: c.Wrap, Timeout: c.Timeout,
		Stdout: output, Stderr: output,
	})
	if err != nil {
		return Verdict{}, err
	}

	node := c.node(args, cwd, outcome.Elapsed, outcome.State)
	switch {
	case outcome.Err == nil:
		node.Passed = true
		node.Stdout = output.String()
		return c.verdict(Verdict{OK: true}, node), nil
	case outcome.TimedOut:
		node.Failed, node.TimedOut = true, true
		node.Message = fmt.Sprintf("%s timed out after %s", c.Cmd, effectiveTimeout(c.Timeout))
		node.Stderr = output.String()
		return c.verdict(Verdict{OK: false, Reason: node.Message, Feedback: output.String()}, node), nil
	}
	feedback := output.String()
	if feedback == "" {
		feedback = outcome.Err.Error()
	}
	node.Failed = true
	node.Message = c.Cmd + " failed"
	node.Stderr = feedback
	return c.verdict(Verdict{OK: false, Reason: node.Message, Feedback: feedback}, node), nil
}

// node is the single leaf a command verifier reports: the command as declared
// (not as wrapped), where it ran, how long it took and how it exited.
func (c *CmdVerifier) node(args []string, cwd string, elapsed time.Duration, state *os.ProcessState) api.VerifyNode {
	command := strings.TrimSpace(strings.Join(append([]string{c.Cmd}, args...), " "))
	exitCode := exitCodeOf(state)
	return api.VerifyNode{
		Name:      command,
		Framework: api.VerifyKindCmd,
		Command:   command,
		WorkDir:   cwd,
		Duration:  elapsed,
		Context:   &api.VerifyNodeContext{Command: command, ExitCode: exitCode, Cwd: cwd},
	}
}

func (c *CmdVerifier) verdict(vd Verdict, node api.VerifyNode) Verdict {
	report := api.NewNodeReport(api.VerifyKindCmd, node.Command, node)
	report.Feedback = vd.Feedback
	vd.Report = &report
	return vd
}
