package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"

	"github.com/flanksource/captain/pkg/ai/callertools"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/ai/tools/exectool"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credproxy"
	"github.com/flanksource/captain/pkg/gitagent"
)

// taskToolset is what a task's agent calls tools through: the supervisor's
// server reached as a pre-issued endpoint, or — when tools also run here — one
// local server holding the sidecar's tools and forwarders for the
// supervisor's. Exactly one of the two is set, or neither.
type taskToolset struct {
	endpoint    *api.CallerToolEndpoint
	definitions []api.ToolDefinition
	// denyRead are the paths the agent must not read while tools here hold
	// credentials: what grants the task's credentials, and what serves them.
	denyRead []string
}

// holdsCredentials reports whether a sidecar tool brings credentials onto this
// host — its own, released ones, or a proxy's secret.
func holdsCredentials(tool api.InlineTool) bool {
	switch tool.Credentials {
	case api.ToolCredentialsSidecar, api.ToolCredentialsSidecarProxied, api.ToolCredentialsProxied:
		return true
	}
	return false
}

// agentDenyRead is everything on this host that would let the agent act with
// more than its tools allow: the task file (tool and release tokens), the
// tools' private directories, the sidecar's keys and enrollment token, and the
// captain config naming them.
func agentDenyRead(taskDir string, relay gitagent.RelayTarget) ([]string, error) {
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return nil, err
	}
	config, err := captainconfig.Path()
	if err != nil {
		return nil, err
	}
	paths := []string{
		filepath.Join(taskDir, gitagent.ControlTaskFile), filepath.Join(taskDir, sidecarToolsDir), config,
		filepath.Join(keysDir, hostKeyName), filepath.Join(keysDir, agentKeyName), filepath.Join(keysDir, dispatchKeyName),
		filepath.Join(keysDir, gitagent.TokenFileName), filepath.Join(keysDir, gitagent.TLSKeyFileName),
	}
	for _, path := range []string{relay.KeyPath, relay.TokenPath} {
		if path != "" && !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// sidecarToolsDir holds each sidecar tool's private directory, beside the
// worktree rather than inside it.
const sidecarToolsDir = "tools"

// agentEnvNames are the variables an agent keeps once tools here hold
// credentials: what a process needs to run, reach the network through the
// host's egress, and authenticate the agent CLI itself. Anything else in the
// sidecar's environment is the sidecar's.
var agentEnvNames = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TZ",
	"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	"CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN",
	"OPENAI_BASE_URL", "CODEX_HOME",
}

// keepAgentEnv reports whether a variable stays in the agent's environment:
// the fixed list, captain's own task variables, and every model provider's
// API key variables.
func keepAgentEnv(name string) bool {
	if slices.Contains(agentEnvNames, name) || strings.HasPrefix(name, "CAPTAIN_") {
		return true
	}
	for _, provider := range api.Providers() {
		if slices.Contains(provider.EnvVars, name) {
			return true
		}
	}
	return false
}

// scrubAgentEnv reduces this process's environment to what the agent keeps.
// Every backend's agent inherits it, so this is the one seam that covers them
// all. It runs after the tools' environments are prepared — they resolved what
// they needed from the sidecar's environment then — and it returns the names
// it removed, for the log.
func scrubAgentEnv() ([]string, error) {
	var removed []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "" || keepAgentEnv(name) {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			return removed, fmt.Errorf("scrub %s from the agent's environment: %w", name, err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// taskToolsetFor serves a task's tools to its agent. Supervisor tools stay the
// supervisor's — it executes them and approves each call — and are reached
// through the task's tools service. Sidecar tools are bound here, in the
// worktree, and resolved against the run's tool policy exactly as the
// supervisor's admission check resolved them. The closer tears all of it down.
func taskToolsetFor(ctx context.Context, opts GitAgentRunTaskOptions, worktree string, payload gitagent.TaskPayload) (taskToolset, func(), error) {
	var closers []func() error
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			_ = closers[i]()
		}
	}
	if payload.Tools == nil && payload.Sidecar == nil {
		return taskToolset{}, closeAll, nil
	}
	runtime, err := hookRuntimeFromConfig(opts.Backend)
	if err != nil {
		return taskToolset{}, closeAll, fmt.Errorf("tools: load the relay configuration: %w", err)
	}
	target, err := gitagent.TaskServiceTarget(opts.Repo, runtime.Relay, opts.Task)
	if err != nil {
		return taskToolset{}, closeAll, fmt.Errorf("tools: %w", err)
	}
	dial := gitagent.Dialer(target, opts.Task)
	var supervisor *api.CallerToolEndpoint
	if payload.Tools != nil {
		local, closeForwarder, err := gitagent.ServeForwarder(ctx, dial, gitagent.ToolsService)
		if err != nil {
			return taskToolset{}, closeAll, fmt.Errorf("tools: %w", err)
		}
		closers = append(closers, closeForwarder)
		supervisor = &api.CallerToolEndpoint{Name: payload.Tools.Name, URL: "http://" + local + payload.Tools.Path, Headers: payload.Tools.Headers}
		if err := supervisor.Validate(); err != nil {
			return taskToolset{}, closeAll, fmt.Errorf("tools: %w", err)
		}
	}
	if payload.Sidecar == nil {
		return taskToolset{endpoint: supervisor}, closeAll, nil
	}
	var denyRead []string
	taskDir := filepath.Dir(worktree)
	for _, tool := range payload.Sidecar.Tools {
		if holdsCredentials(tool) {
			if denyRead, err = agentDenyRead(taskDir, runtime.Relay); err != nil {
				return taskToolset{}, closeAll, fmt.Errorf("tools: %w", err)
			}
			break
		}
	}
	definitions, closeSidecar, err := sidecarToolDefinitions(ctx, worktree, filepath.Join(taskDir, sidecarToolsDir), payload.Sidecar, dial)
	closers = append(closers, closeSidecar)
	if err != nil {
		return taskToolset{}, closeAll, err
	}
	if supervisor != nil {
		forwarded, closeForward, err := callertools.Forward(ctx, *supervisor)
		if err != nil {
			return taskToolset{}, closeAll, err
		}
		closers = append(closers, closeForward)
		for _, definition := range forwarded {
			if slices.ContainsFunc(definitions, func(d api.ToolDefinition) bool { return d.Name == definition.Name }) {
				return taskToolset{}, closeAll, fmt.Errorf("tools: %q is both a sidecar tool and a supervisor tool", definition.Name)
			}
		}
		definitions = append(definitions, forwarded...)
	}
	return taskToolset{definitions: definitions, denyRead: denyRead}, closeAll, nil
}

// sidecarToolDefinitions binds the shipped sidecar tools in the worktree and
// prepares their environments, fetching a sidecar-proxied tool's grant values
// from its release once.
func sidecarToolDefinitions(ctx context.Context, worktree, privateRoot string, shipped *gitagent.TaskSidecarTools, dial gitagent.ServiceDialer) ([]api.ToolDefinition, func() error, error) {
	release := func(ctx context.Context, tool string, grants []credproxy.Grant) ([]credproxy.Grant, error) {
		located, ok := shipped.Releases[tool]
		if !ok {
			return nil, fmt.Errorf("tools.%s is sidecar-proxied, but the task ships no credential release for it", tool)
		}
		local, closeForwarder, err := gitagent.ServeForwarder(ctx, dial, located.Service)
		if err != nil {
			return nil, err
		}
		defer func() { _ = closeForwarder() }()
		return exectool.FetchGrantRelease(ctx, "http://"+local+"/", located.Token, grants)
	}
	proxies := func(ctx context.Context, tool string) (exectool.ProxyEndpoints, func() error, error) {
		located, ok := shipped.Proxies[tool]
		if !ok {
			return exectool.ProxyEndpoints{}, nil, fmt.Errorf("tools.%s is proxied, but the task ships no proxies for it", tool)
		}
		return forwardToolProxies(ctx, dial, located)
	}
	workspace := exectool.NewWorkspace(exectool.WorkspaceOptions{
		Cwd: worktree, ReleaseGrants: release, SupervisorProxies: proxies, PrivateRoot: privateRoot,
	})
	definitions, err := exectool.Definitions(shipped.Tools, workspace)
	if err != nil {
		return nil, workspace.Close, fmt.Errorf("tools: %w", err)
	}
	resolved, err := aitools.ResolveDefinitions(definitions, aitools.ResolveOptions{Preferences: shipped.Preferences, Policy: shipped.Policy})
	if err != nil {
		return nil, workspace.Close, fmt.Errorf("tools: %w", err)
	}
	for _, definition := range resolved {
		if definition.NeedsApproval() {
			return nil, workspace.Close, fmt.Errorf("tools.%s needs approval, which nobody on the sidecar can give; the supervisor should have refused it", definition.Name)
		}
	}
	if err := workspace.Prepare(ctx); err != nil {
		return nil, workspace.Close, fmt.Errorf("tools: %w", err)
	}
	return resolved, workspace.Close, nil
}

// forwardToolProxies relays a proxied tool's supervisor proxies to loopback
// here, for the lifetime of the tool's environment.
func forwardToolProxies(ctx context.Context, dial gitagent.ServiceDialer, located gitagent.TaskToolProxies) (exectool.ProxyEndpoints, func() error, error) {
	endpoints := exectool.ProxyEndpoints{EgressSecret: located.EgressSecret, CAPEM: located.CAPEM, KubeToken: located.KubeToken}
	var closers []func() error
	closeAll := func() error {
		var errs []error
		for i := len(closers) - 1; i >= 0; i-- {
			errs = append(errs, closers[i]())
		}
		return errors.Join(errs...)
	}
	forward := func(service string) (string, error) {
		local, closer, err := gitagent.ServeForwarder(ctx, dial, service)
		if err != nil {
			return "", fmt.Errorf("forward %s: %w", service, err)
		}
		closers = append(closers, closer)
		return local, nil
	}
	if located.EgressService != "" {
		local, err := forward(located.EgressService)
		if err != nil {
			return endpoints, closeAll, err
		}
		endpoints.Egress = local
	}
	if located.KubeService != "" {
		local, err := forward(located.KubeService)
		if err != nil {
			return endpoints, closeAll, err
		}
		endpoints.KubeURL = "http://" + local
	}
	return endpoints, closeAll, nil
}
