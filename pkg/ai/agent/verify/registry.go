package verify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
)

// Kind names a family of verifier — one field of api.Verify and the factory that
// turns that field into hooks.
type Kind string

const (
	KindCmd     Kind = "cmd"
	KindPrompt  Kind = "prompt"
	KindFixture Kind = "fixture"
)

// kindOrder is the order a workflow's checks run in: the cheap deterministic
// commands first, then the judges and fixtures that cost a model call or a
// whole test run, so a run that is going to fail fails on the fast check.
var kindOrder = []Kind{KindCmd, KindPrompt, KindFixture}

// Options is what every factory is given besides the spec: the provider a judge
// executes on, the confinement and bounds every child process inherits, and an
// optional sink for live progress snapshots.
type Options struct {
	// Provider judges prompt hooks. Required when Verify.Prompts is non-empty.
	Provider ai.Provider
	// Env, Wrap and Timeout apply to every verifier that starts a process: the
	// allowlisted environment, the confinement seam (a receive path must never
	// exec agent-authored input bare on the host — R5.2), and the wall clock.
	Env     []string
	Wrap    CommandWrapFunc
	Timeout time.Duration
	// Progress receives each coalesced in-flight snapshot a verifier reports.
	// Nil means the caller wants only the final verdict.
	Progress func(api.VerifyReport)
	// RunSpec is the resolved spec of the run these checks belong to — the model,
	// permissions, budget and workflow it was started under — and is read-only to
	// a factory. A factory that runs an agent of its own (a fixture grader
	// judging a document's acceptance criteria) inherits the run's posture from
	// it; without it such a grader has to invent a model and a permission mode,
	// and grades outside the bounds the run declared. Nil when the checks are
	// driven with no run behind them.
	RunSpec *api.Spec
}

// Factory builds the hooks one kind contributes. It returns *Plugin rather than
// the runner's []any so a caller that drives verifiers out of loop — the
// git-agent receive path, `captain verify` — keeps the typed handle.
type Factory func(ctx context.Context, spec api.Verify, opts Options) ([]*Plugin, error)

var (
	registryMu sync.RWMutex
	factories  = map[Kind]Factory{}
)

func init() {
	Register(KindCmd, cmdFactory)
	Register(KindPrompt, promptFactory)
}

// Register installs the factory for one kind. Registering a kind twice panics:
// two factories for one field means half the declared checks silently never run,
// and which half depends on init order.
func Register(kind Kind, f Factory) {
	if f == nil {
		panic(fmt.Sprintf("verify: nil factory registered for kind %q", kind))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := factories[kind]; exists {
		panic(fmt.Sprintf("verify: a factory for kind %q is already registered", kind))
	}
	factories[kind] = f
}

// Registered reports whether a kind has a factory, so a host can install its own
// only when nothing has claimed the kind yet.
func Registered(kind Kind) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := factories[kind]
	return ok
}

// Unregister removes a kind's factory and reports whether one was installed.
//
// It is a test and host seam, not part of the dispatch path. The registry is
// process-global, so a spec that installs a factory has to take it back out or
// the next spec inherits it; and a host that owns the whole process — it linked
// the fixture runner, nothing else can be mid-verification — may replace a kind
// by unregistering it first, since Register refuses to overwrite a live one.
func Unregister(kind Kind) bool {
	registryMu.Lock()
	defer registryMu.Unlock()
	_, existed := factories[kind]
	delete(factories, kind)
	return existed
}

func factoryFor(kind Kind) (Factory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := factories[kind]
	return f, ok
}

// HooksFor builds the generate→verify loop's Verify hooks from a spec's
// Workflow, dispatching each declared kind to its registered factory in
// kindOrder. Returns nil when there is nothing to verify.
//
// It returns []any — the runner's heterogeneous hook list — and every plugin it
// returns carries opts.Progress, so a factory cannot forget to wire the sink.
//
// A declared check with no factory is an error, never an empty hook list: a
// workflow whose only verification is a fixture would otherwise produce zero
// hooks, and a run with zero verify hooks passes vacuously.
func HooksFor(ctx context.Context, wf *api.Workflow, opts Options) ([]any, error) {
	if wf == nil || wf.Verify == nil {
		return nil, nil
	}
	if strings.TrimSpace(wf.Verify.Fixture) != "" && !Registered(KindFixture) {
		return nil, fmt.Errorf("workflow.verify.fixture declared but no fixture verifier is registered " +
			"(link a fixture runner in-process or set verify.fixtureRunner in ~/.captain.yaml)")
	}
	var hooks []any
	for _, kind := range kindOrder {
		factory, ok := factoryFor(kind)
		if !ok {
			continue
		}
		plugins, err := factory(ctx, *wf.Verify, opts)
		if err != nil {
			return nil, err
		}
		for _, p := range plugins {
			p.OnProgress(opts.Progress)
			hooks = append(hooks, p)
		}
	}
	return hooks, nil
}

// ValidatePromptDeclarations loads every declared judge prompt before a run
// constructs its provider. This keeps a broken workflow attributable to the
// prompt declaration even when the selected provider is unavailable.
func ValidatePromptDeclarations(wf *api.Workflow) error {
	if wf == nil || wf.Verify == nil {
		return nil
	}
	for i, path := range wf.Verify.Prompts {
		path = strings.TrimSpace(path)
		if path == "" {
			return fmt.Errorf("workflow.verify.prompts[%d] is empty", i)
		}
		if _, err := prompt.LoadFile(path); err != nil {
			return fmt.Errorf("verify prompt %q: %w", path, err)
		}
	}
	return nil
}

// DeclaresExec reports whether the workflow declares a check that starts a
// process — a shell command or a fixture handed to an external runner. A
// receive path asks before it has any hooks, because the confinement wrapper is
// built against the materialized tree and must exist before the checks do.
func DeclaresExec(wf *api.Workflow) bool {
	if wf == nil || wf.Verify == nil {
		return false
	}
	if strings.TrimSpace(wf.Verify.Fixture) != "" {
		return true
	}
	for _, cmd := range wf.Verify.Commands {
		if strings.TrimSpace(cmd) != "" {
			return true
		}
	}
	return false
}

// cmdFactory turns each verify command into a pass/fail check whose failure
// output drives the re-run. A blank entry is skipped rather than run as an empty
// shell command.
func cmdFactory(_ context.Context, spec api.Verify, opts Options) ([]*Plugin, error) {
	var plugins []*Plugin
	for _, cmd := range spec.Commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		plugins = append(plugins, New("verify:"+cmd, &CmdVerifier{
			Cmd: "sh", Args: []string{"-c", cmd},
			Env: opts.Env, Wrap: opts.Wrap, Timeout: opts.Timeout,
		}))
	}
	return plugins, nil
}

// promptFactory builds the LLM-judge hooks from Verify.Prompts: each entry is a
// .prompt template whose output schema is {ok, reason, feedback}, judged by the
// run's provider.
//
// A prompt that fails to load is an error, not a skipped hook: a declared check
// that silently never runs is a false accept.
func promptFactory(_ context.Context, spec api.Verify, opts Options) ([]*Plugin, error) {
	if len(spec.Prompts) == 0 {
		return nil, nil
	}
	if opts.Provider == nil {
		return nil, fmt.Errorf("verify prompts declared but no provider available to judge them")
	}
	var plugins []*Plugin
	for i, path := range spec.Prompts {
		path = strings.TrimSpace(path)
		if path == "" {
			// Same rule as api.Workflow.Validate: a blank entry is a broken
			// declaration, and skipping it would silently drop a configured check
			// for callers that never ran Validate.
			return nil, fmt.Errorf("workflow.verify.prompts[%d] is empty", i)
		}
		tmpl, err := prompt.LoadFile(path)
		if err != nil {
			return nil, fmt.Errorf("verify prompt %q: %w", path, err)
		}
		if err := rejectJudgeOverrides(path, tmpl, opts.Provider); err != nil {
			return nil, err
		}
		plugins = append(plugins, New("judge:"+path, &LLMJudgeVerifier{Provider: opts.Provider, Prompt: tmpl}))
	}
	return plugins, nil
}

// rejectJudgeOverrides refuses judge frontmatter the hook cannot honour. A
// judge executes on the run's provider, so a declared sandbox — relocating or
// not — and a model different from the provider's would both be silently
// ignored, which is exactly the downgrade issue #39 forbids (R5.4: a hook
// prompt declaring a relocating sandbox is a validation error, never a silent
// fallback).
func rejectJudgeOverrides(path string, tmpl *prompt.Template, provider ai.Provider) error {
	probe, _, err := tmpl.Render(map[string]any{"cwd": "", "changed": []string{}}, nil)
	if err != nil {
		return fmt.Errorf("verify prompt %q: %w", path, err)
	}
	if probe.Sandbox != nil {
		return fmt.Errorf("verify prompt %q declares a sandbox; judge hooks run on the run's provider and cannot relocate", path)
	}
	if declared := strings.TrimSpace(probe.Name); declared != "" && declared != provider.GetModel() {
		return fmt.Errorf("verify prompt %q declares model %q but judge hooks run on the run's provider (%s); remove the model or match it",
			path, declared, provider.GetModel())
	}
	return nil
}
