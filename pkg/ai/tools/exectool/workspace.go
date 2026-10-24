package exectool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// WorkspaceOptions describe where a run's inline tools execute.
type WorkspaceOptions struct {
	// Cwd is the run's working directory before setup relocates it.
	Cwd string
	// Setup is the run's setup; tools inherit its dotenv files, env vars and
	// connections as their credentials mode allows.
	Setup *shell.Setup
	// SandboxWrap confines placement: sandbox tools inside the run's sandbox.
	// Nil means the run has no sandbox a tool can be placed in.
	SandboxWrap shell.WrapFunc
}

// Workspace is the run-scoped environment of a run's inline tools. A run's
// setup is applied after its tools are bound, so the environments are prepared
// at PreRun — ordered after setup.Plugin — once per run rather than per call:
// connections are materialized once, credential files land in a private
// directory instead of the workspace, and proxies start once. Post at PhaseRun
// tears all of it down, whatever the run's outcome.
type Workspace struct {
	wrap shell.WrapFunc

	mu       sync.Mutex
	cwd      string
	run      shell.Setup
	tools    map[string]boundTool
	prepared map[string]*environment
}

type boundTool struct {
	tool        api.InlineTool
	credentials api.ToolCredentials
}

// environment is one tool's prepared execution environment.
type environment struct {
	setup   *shell.SetupResult
	wrap    shell.WrapFunc
	closers []func() error
}

func NewWorkspace(opts WorkspaceOptions) *Workspace {
	w := &Workspace{wrap: opts.SandboxWrap, tools: map[string]boundTool{}}
	w.setRun(opts.Cwd, opts.Setup)
	return w
}

func (w *Workspace) Name() string { return "inline-tools" }

// PreRun prepares every bound tool's environment in the workspace setup left.
func (w *Workspace) PreRun(hc *agent.HookContext) error {
	cwd := hc.Workspace().Cwd
	if cwd == "" {
		cwd = hc.Request.Cwd()
	}
	w.setRun(cwd, hc.Request.Setup)
	return w.prepare(hc)
}

func (w *Workspace) Phases() []agent.Phase { return []agent.Phase{agent.PhaseRun} }

func (w *Workspace) Post(*agent.HookContext, agent.Phase) error { return w.Close() }

// Close tears down every prepared environment. It is idempotent.
func (w *Workspace) Close() error {
	w.mu.Lock()
	prepared := w.prepared
	w.prepared = nil
	w.mu.Unlock()
	var errs []error
	for _, name := range sortedKeys(prepared) {
		if err := prepared[name].close(); err != nil {
			errs = append(errs, fmt.Errorf("tools.%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func (w *Workspace) setRun(cwd string, setup *shell.Setup) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cwd = cwd
	w.run = shell.Setup{}
	if setup != nil {
		w.run = shell.Setup{BaseDir: setup.BaseDir, DotEnv: setup.DotEnv, EnvVars: setup.EnvVars, Connections: setup.Connections}
	}
}

// bind registers a tool and resolves its credentials mode against the run's
// setup, so a contradiction fails when the tool is bound, before the run.
func (w *Workspace) bind(name string, tool api.InlineTool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	credentials, err := tool.ResolveCredentials(w.run)
	if err != nil {
		return err
	}
	if tool.Placement.Effective() == api.ToolPlacementSandbox && w.wrap == nil {
		return fmt.Errorf("placement: sandbox, but the run has no sandbox wrapper to confine it")
	}
	w.tools[name] = boundTool{tool: tool, credentials: credentials}
	return nil
}

func (w *Workspace) environment(name string) (*environment, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	env, ok := w.prepared[name]
	if !ok {
		return nil, fmt.Errorf("the tool environment is not prepared: the run's inline-tools hook has not run, or the run has ended")
	}
	return env, nil
}

func (w *Workspace) prepare(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.prepared != nil {
		return fmt.Errorf("inline tool environments are already prepared")
	}
	prepared := map[string]*environment{}
	for _, name := range sortedKeys(w.tools) {
		env, err := w.prepareTool(ctx, name, w.tools[name])
		if err != nil {
			for _, done := range prepared {
				_ = done.close()
			}
			return fmt.Errorf("tools.%s: prepare: %w", name, err)
		}
		prepared[name] = env
	}
	w.prepared = prepared
	return nil
}

func (w *Workspace) prepareTool(ctx context.Context, name string, bound boundTool) (*environment, error) {
	credsDir, err := os.MkdirTemp("", "captain-tool-"+name+"-")
	if err != nil {
		return nil, err
	}
	env := &environment{closers: []func() error{func() error { return os.RemoveAll(credsDir) }}}
	if bound.tool.Placement.Effective() == api.ToolPlacementSandbox {
		env.wrap = w.wrap
	}
	dbctx := dbcontext.NewContext(ctx)
	setup, extra, err := w.toolSetup(dbctx, bound, credsDir, env)
	if err != nil {
		_ = env.close()
		return nil, err
	}
	resolved, err := setup.Resolve(w.cwd)
	if err != nil {
		_ = env.close()
		return nil, err
	}
	exec := resolved.ToExec()
	exec.CredentialsDir = credsDir
	prepared, err := shell.SetupEnv(dbctx, exec)
	if err != nil {
		_ = env.close()
		return nil, err
	}
	env.closers = append(env.closers, prepared.Cleanup)
	prepared.Env = append(prepared.Env, extra...)
	env.setup = prepared
	return env, nil
}

// toolSetup layers the tool's own setup over what its credentials mode lets it
// inherit from the run, returning any extra environment a proxy contributes.
// Dotenv files and static env vars are configuration and always inherited; the
// tool's own win on conflict.
func (w *Workspace) toolSetup(ctx dbcontext.Context, bound boundTool, credsDir string, env *environment) (shell.Setup, []string, error) {
	tool, run := bound.tool, w.run
	setup := shell.Setup{
		Cwd:     tool.Cwd,
		BaseDir: tool.BaseDir,
		DotEnv:  append(slices.Clone(run.DotEnv), tool.DotEnv...),
	}
	if setup.BaseDir == "" {
		setup.BaseDir = run.BaseDir
	}
	switch bound.credentials {
	case api.ToolCredentialsDirect:
		setup.EnvVars = append(slices.Clone(run.EnvVars), tool.EnvVars...)
		setup.Connections = run.Connections
		if len(api.DeclaredConnections(tool.Connections)) > 0 {
			setup.Connections = tool.Connections
		}
		return setup, nil, nil
	case api.ToolCredentialsProxied:
		setup.EnvVars = append(staticEnvVars(run.EnvVars), tool.EnvVars...)
		kube := run.Connections.Kubernetes
		if tool.Connections.Kubernetes != nil {
			kube = tool.Connections.Kubernetes
		}
		extra, closers, err := startProxies(ctx, proxyOptions{dir: credsDir, baseDir: setup.BaseDir, kubernetes: kube, grants: tool.Grants})
		env.closers = append(env.closers, closers...)
		return setup, extra, err
	default:
		setup.EnvVars = append(staticEnvVars(run.EnvVars), tool.EnvVars...)
		return setup, nil, nil
	}
}

// close runs the closers newest first: a proxy stops before the files it
// serves from are removed.
func (e *environment) close() error {
	var errs []error
	for i := len(e.closers) - 1; i >= 0; i-- {
		if err := e.closers[i](); err != nil {
			errs = append(errs, err)
		}
	}
	e.closers = nil
	return errors.Join(errs...)
}

func staticEnvVars(vars []types.EnvVar) []types.EnvVar {
	var static []types.EnvVar
	for _, env := range vars {
		if env.ValueFrom == nil || env.ValueFrom.IsEmpty() {
			static = append(static, env)
		}
	}
	return static
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
