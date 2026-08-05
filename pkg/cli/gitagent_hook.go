package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"gopkg.in/yaml.v3"
)

type GitAgentHookOptions struct {
	Hook    string `args:"true" help:"pre-receive or post-receive"`
	Repo    string `flag:"repo" help:"Receiving bare repository"`
	Role    string `flag:"role" help:"Receiver role: sidecar or mailbox"`
	Backend string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
	Config  string `flag:"config" help:"Config file to read; hooks cannot rely on $HOME, which belongs to whoever pushed"`
}

// RunGitAgentHook is the shim entrypoint: admission, hook sets, relay and
// integration, with the runtime assembled from the backend's config block.
func RunGitAgentHook(ctx context.Context, opts GitAgentHookOptions) (any, error) {
	if strings.TrimSpace(opts.Config) != "" {
		captainconfig.SetPath(opts.Config)
	}
	runtime, err := hookRuntimeFromConfig(opts.Backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain: %v\n", err)
		return nil, err
	}
	wrap, err := gitagent.ResolveHookWrap(runtime.HookSandbox)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain: %v\n", err)
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	configPath := strings.TrimSpace(opts.Config)
	host := gitagent.HookHost{
		Runtime: runtime,
		Wrap:    wrap,
		// With no agentCommand configured, the sidecar still launches a real
		// agent: this binary, working the task in the prepared worktree. The
		// alternative — launching nothing — leaves the supervisor waiting out
		// its whole budget on work that never started.
		DefaultAgentCommand: func(repo, task string) string {
			return DefaultAgentCommand(exe, repo, task, configPath)
		},
	}
	role := gitagent.ReceiverRole(opts.Role)
	switch opts.Hook {
	case "pre-receive":
		return nil, gitagent.RunPreReceive(ctx, opts.Repo, role, host, os.Stdin, os.Stderr)
	case "post-receive":
		return nil, gitagent.RunPostReceive(ctx, opts.Repo, role, host, os.Stdin)
	default:
		return nil, fmt.Errorf("unknown hook %q", opts.Hook)
	}
}

// hookRuntimeFromConfig assembles the receiver runtime from the backend's
// options block: the two hook-set workflows, the confinement sandbox for exec
// hooks, the agent launch command, the integration target, and the relay
// endpoint recorded at enrollment.
func hookRuntimeFromConfig(backendName string) (gitagent.HookRuntime, error) {
	var rt gitagent.HookRuntime
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return rt, err
	}
	backend, ok := cfg.Sandbox.Backends[backendName]
	if !ok {
		return rt, nil // no config: admission still runs, hook sets are empty
	}
	if hooks, ok := backend.Options["hooks"].(map[string]any); ok {
		if rt.SidecarWorkflow, err = decodeWorkflow(hooks["sidecar"]); err != nil {
			return rt, fmt.Errorf("hooks.sidecar: %w", err)
		}
		if rt.SupervisorWorkflow, err = decodeWorkflow(hooks["supervisor"]); err != nil {
			return rt, fmt.Errorf("hooks.supervisor: %w", err)
		}
	}
	rt.HookSandbox, _ = backend.Options["hookSandbox"].(string)
	rt.AgentCommand, _ = backend.Options["agentCommand"].(string)
	rt.RealRepo, _ = backend.Options["repo"].(string)
	if supervisor, ok := backend.Options["supervisor"].(map[string]any); ok {
		url, _ := supervisor["url"].(string)
		hostFP, _ := supervisor["hostFingerprint"].(string)
		keysDir, err := gitAgentKeysDir()
		if err != nil {
			return rt, err
		}
		exe, err := os.Executable()
		if err != nil {
			return rt, err
		}
		rt.Relay = gitagent.RelayTarget{
			URL:             url,
			HostFingerprint: hostFP,
			KeyPath:         filepath.Join(keysDir, "agent_ed25519"),
			SSHCommand:      exe + " sandbox git-agent ssh",
		}
	}
	return rt, nil
}

// decodeWorkflow converts a YAML-decoded options value into an api.Workflow
// via a JSON round-trip, so the receiver runs the exact schema the local run
// path declares (A5.1).
func decodeWorkflow(v any) (*api.Workflow, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := yaml.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	var wf api.Workflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	if err := wf.Validate(); err != nil {
		return nil, err
	}
	return &wf, nil
}
