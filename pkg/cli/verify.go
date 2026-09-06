package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/api"
	clickyapi "github.com/flanksource/clicky/api"
)

// VerifyOptions runs a workflow's verification stage on its own: the same
// checks the generate→verify loop votes with, against a tree that is already
// written. It is how a definition of done is exercised without spending a
// generation on it — locally before a push, or by a host wiring captain's
// checks into its own pipeline.
//
// There is deliberately no --json flag: clicky already binds --json/--format
// globally, and the result renders through that pipeline.
type VerifyOptions struct {
	Fixture  string   `flag:"fixture" help:"Fixture document run by the configured verify.fixtureRunner"`
	Commands []string `flag:"command" help:"Shell command run as a pass/fail check (repeatable)" short:"c"`
	Prompts  []string `flag:"prompt" help:"LLM-judge .prompt template, judged by the run's provider (repeatable)"`
	Cwd      string   `flag:"cwd" help:"Directory the checks run in" default:"."`
	Timeout  string   `flag:"timeout" help:"Wall-clock bound per check" default:"10m"`

	AIProviderOptions
}

// VerifyResult is what one `captain verify` run reports: the verdict, and every
// check's typed report exactly as a Verify hook produces it inside a run.
type VerifyResult struct {
	Passed  bool               `json:"passed" pretty:"label=Passed"`
	Reports []api.VerifyReport `json:"reports"`
	Summary api.VerifySummary  `json:"summary"`
}

// verifyRejected carries the reports through the failure path. clicky renders
// an error that implements a rendering interface through the same format
// pipeline as a success and still exits non-zero — so a failing verification
// prints its reports rather than a bare sentence.
type verifyRejected struct{ VerifyResult }

func (e verifyRejected) Error() string {
	failed := 0
	for _, report := range e.Reports {
		if !report.Passed {
			failed++
		}
	}
	return fmt.Sprintf("verification failed: %d of %d checks did not pass", failed, len(e.Reports))
}

func RunVerify(ctx context.Context, opts VerifyOptions) (any, error) {
	cwd, err := filepath.Abs(firstNonEmpty(strings.TrimSpace(opts.Cwd), "."))
	if err != nil {
		return nil, err
	}
	wf, err := opts.workflow()
	if err != nil {
		return nil, err
	}
	timeout, err := verifyTimeout(opts.Timeout)
	if err != nil {
		return nil, err
	}
	provider, model, err := opts.judgeProvider()
	if err != nil {
		return nil, err
	}
	if provider != nil {
		defer closeProvider(provider)
	}

	// The spec this verification runs under, so a verifier that grades with an
	// agent of its own inherits the model resolved here instead of choosing one.
	spec := &api.Spec{Model: model, Workflow: wf}
	hooks, err := verify.HooksFor(ctx, wf, verify.Options{Provider: provider, Timeout: timeout, RunSpec: spec})
	if err != nil {
		return nil, err
	}
	if len(hooks) == 0 {
		return nil, fmt.Errorf("nothing to verify: pass --command, --prompt or --fixture")
	}

	result := VerifyResult{Passed: true}
	for _, hook := range hooks {
		plugin, ok := hook.(*verify.Plugin)
		if !ok {
			return nil, fmt.Errorf("unexpected hook type %T", hook)
		}
		report, err := runVerifyPlugin(ctx, plugin, cwd)
		if err != nil {
			// A check that could not reach a verdict is not a failing check: the
			// run has no answer, and saying "failed" would invent one.
			return nil, fmt.Errorf("%s: %w", plugin.Name(), err)
		}
		result.Reports = append(result.Reports, report)
		result.Passed = result.Passed && report.Passed
		result.Summary = api.AddSummaries(result.Summary, report.Summary)
	}
	if !result.Passed {
		return result, verifyRejected{result}
	}
	return result, nil
}

// runVerifyPlugin drives one check out of loop — the same path the git-agent
// receive side uses — and completes its report with the hook's name.
func runVerifyPlugin(ctx context.Context, plugin *verify.Plugin, cwd string) (api.VerifyReport, error) {
	started := time.Now()
	vd, err := plugin.Verifier().Verify(ctx, cwd, nil)
	if err != nil {
		return api.VerifyReport{}, err
	}
	if vd.Report == nil {
		// Every verifier captain ships reports; a host-supplied one need not.
		synthesised := api.NewNodeReport(api.VerifyKindFunc, plugin.Name(), api.VerifyNode{
			Name: plugin.Name(), Passed: vd.OK, Failed: !vd.OK, Message: vd.Reason,
			Duration: time.Since(started),
		})
		vd.Report = &synthesised
	}
	report := *vd.Report
	if report.Name == "" {
		report.Name = plugin.Name()
	}
	return report, nil
}

// workflow is the api.Workflow the flags describe: the same declaration a spec
// carries, so the CLI and a run verify through one code path.
func (o VerifyOptions) workflow() (*api.Workflow, error) {
	spec := &api.Verify{Commands: o.Commands, Prompts: o.Prompts}
	if path := strings.TrimSpace(o.Fixture); path != "" {
		document, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read fixture %s: %w", path, err)
		}
		spec.Fixture = string(document)
	}
	wf := &api.Workflow{Verify: spec}
	if err := wf.Validate(); err != nil {
		return nil, err
	}
	return wf, nil
}

// judgeProvider builds the provider the LLM-judge prompts execute on, and only
// then: a verification made of commands and fixtures needs no model, and
// constructing one would demand credentials the run does not use. It returns the
// resolved model alongside, because that is what the run's spec declares — a
// zero model when nothing here needs one.
func (o VerifyOptions) judgeProvider() (ai.Provider, api.Model, error) {
	if len(o.Prompts) == 0 {
		return nil, api.Model{}, nil
	}
	resolved, err := resolveInvocation(AIRuntimeOptions{AIProviderOptions: o.AIProviderOptions}, nil)
	if err != nil {
		return nil, api.Model{}, err
	}
	cfg := resolved.Config
	logRuntimeWarnings(resolved.Resolution.Warnings)
	if cfg.Model.Name == "" {
		return nil, api.Model{}, fmt.Errorf("--prompt needs a model to judge with: pass --model or run 'captain configure'")
	}
	provider, err := ai.NewProvider(cfg)
	if err != nil {
		return nil, api.Model{}, err
	}
	wrapped, err := middleware.Wrap(provider, middleware.WithLogging())
	if err != nil {
		return nil, api.Model{}, err
	}
	return wrapped, cfg.Model, nil
}

func verifyTimeout(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --timeout %q: %w", value, err)
	}
	return timeout, nil
}

// Pretty renders one line per check — the verdict first, then what it was —
// with a failing check's feedback beneath it, since that is the whole reason
// the command was run.
func (r VerifyResult) Pretty() clickyapi.Text {
	text := clickyapi.Text{}
	for i, report := range r.Reports {
		if i > 0 {
			text = text.Append("\n")
		}
		icon, style := "✓", "text-green-500 font-medium"
		if !report.Passed {
			icon, style = "✗", "text-red-500 font-medium"
		}
		text = text.Append(icon+" ", style).Append(report.Name, "text-muted")
		if reason := strings.TrimSpace(report.Reason); reason != "" {
			text = text.Append(" — " + reason)
		}
		if feedback := strings.TrimSpace(report.Feedback); feedback != "" && !report.Passed {
			text = text.Append("\n" + feedback)
		}
	}
	return text
}

// InstallFixtureVerifier registers the configured external fixture runner as
// the `fixture` verifier, so a workflow that declares a fixture is dispatched
// rather than silently contributing no hooks. Called once at CLI startup; a
// host that linked its own fixture runner in-process keeps it.
func InstallFixtureVerifier() error {
	cfg, _, err := LoadCaptainConfigOnce()
	if err != nil {
		return err
	}
	if len(cfg.Verify.FixtureRunner) == 0 || verify.Registered(verify.KindFixture) {
		return nil
	}
	verify.Register(verify.KindFixture, externalFixtureFactory(cfg.Verify.FixtureRunner))
	return nil
}

// externalFixtureFactory builds the one hook a declared fixture contributes:
// the configured runner, handed the fixture document and the run's bounds.
func externalFixtureFactory(command []string) verify.Factory {
	return func(_ context.Context, spec api.Verify, opts verify.Options) ([]*verify.Plugin, error) {
		if strings.TrimSpace(spec.Fixture) == "" {
			return nil, nil
		}
		return []*verify.Plugin{verify.New("fixture", &verify.ExternalVerifier{
			Command: command, Fixture: spec.Fixture,
			Timeout: opts.Timeout, Env: opts.Env, Wrap: opts.Wrap,
		})}, nil
	}
}
