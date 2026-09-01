package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
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
	role := gitagent.ReceiverRole(opts.Role)
	if role != gitagent.RoleSidecar && role != gitagent.RoleMailbox {
		return nil, fmt.Errorf("unknown receiver role %q", opts.Role)
	}
	runtime, err := hookRuntimeFromConfig(opts.Backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain: %v\n", err)
		return nil, err
	}
	if role == gitagent.RoleMailbox {
		binding, err := gitagent.LoadMailboxBinding(opts.Repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "captain: %v\n", err)
			return nil, err
		}
		runtime.RealRepo = binding.Repository
	}
	wrapFor, err := gitagent.ResolveHookWrap(runtime.HookSandbox, opts.Repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain: %v\n", err)
		return nil, err
	}
	var judge ai.Provider
	if opts.Hook == "pre-receive" {
		judge, err = hookJudgeProvider(runtime)
		if err != nil {
			fmt.Fprintf(os.Stderr, "captain: %v\n", err)
			return nil, err
		}
	}
	defer closeProvider(judge)
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	configPath := strings.TrimSpace(opts.Config)
	host := gitagent.HookHost{
		Runtime: runtime,
		Judge:   judge,
		WrapFor: wrapFor,
		// With no agentCommand configured, the sidecar still launches a real
		// agent: this binary, working the task in the prepared worktree. The
		// alternative — launching nothing — leaves the supervisor waiting out
		// its whole budget on work that never started.
		DefaultAgentCommand: func(repo, task string) string {
			command := DefaultAgentCommand(exe, repo, task, configPath)
			if backend := strings.TrimSpace(opts.Backend); backend != "" {
				command += fmt.Sprintf(" --backend %q", backend)
			}
			return command
		},
	}
	switch opts.Hook {
	case "pre-receive":
		return nil, gitagent.RunPreReceive(ctx, opts.Repo, role, host, os.Stdin, os.Stderr)
	case "post-receive":
		return nil, gitagent.RunPostReceive(ctx, opts.Repo, role, host, os.Stdin)
	default:
		return nil, fmt.Errorf("unknown hook %q", opts.Hook)
	}
}

// hookJudgeProvider builds the local provider used by receiver-side prompt
// checks. It pins sandboxing to none because the hook is already executing at
// the remote receiver; selecting git-agent here would recursively dispatch.
func hookJudgeProvider(runtime gitagent.HookRuntime) (ai.Provider, error) {
	if !runtime.RequiresJudge() {
		return nil, nil
	}
	cfg, err := (AIProviderOptions{Sandbox: "none"}).ToConfig()
	if err != nil {
		return nil, fmt.Errorf("configure hook judge: %w", err)
	}
	if strings.TrimSpace(cfg.Model.Name) == "" {
		return nil, fmt.Errorf("verify prompts declared but no model is configured for the hook judge")
	}
	provider, err := ai.NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("create hook judge: %w", err)
	}
	wrapped, err := middleware.Wrap(provider, middleware.WithLogging(), middleware.WithSchemaValidation(cfg))
	if err != nil {
		closeProvider(provider)
		return nil, fmt.Errorf("wrap hook judge: %w", err)
	}
	return wrapped, nil
}

// hookRuntimeFromConfig assembles host-wide receiver settings from the
// backend. Repository integration is mailbox-local and is resolved separately
// from the binding beside that mailbox's task state.
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
	if supervisor, ok := backend.Options["supervisor"].(map[string]any); ok {
		url, _ := supervisor["url"].(string)
		hostFP, _ := supervisor["hostFingerprint"].(string)
		tokenPath, _ := supervisor["tokenPath"].(string)
		rt.Agent, _ = supervisor["agent"].(string)
		keysDir, err := gitAgentKeysDir()
		if err != nil {
			return rt, err
		}
		exe, err := os.Executable()
		if err != nil {
			return rt, err
		}
		if tokenPath == "" {
			tokenPath = filepath.Join(keysDir, gitagent.TokenFileName)
		}
		rt.Relay = gitagent.RelayTarget{
			URL:             url,
			HostFingerprint: hostFP,
			KeyPath:         filepath.Join(keysDir, agentKeyName),
			SSHCommand:      gitagent.SSHTransportCommand(exe),
			// The path, never the credential: this struct is serialized into
			// hooks.json, which every hook process can read.
			TokenPath: tokenPath,
			CAPath:    supervisorCAPath(supervisor, keysDir),
		}
		rt.Relay.PinnedPublicKey, _ = supervisor["pinnedPubkey"].(string)
	}
	return rt, nil
}

// decodeWorkflow converts a YAML-decoded options value into an api.Workflow
// via a JSON round-trip, so the receiver runs the exact schema the local run
// path declares (A5.1).
// supervisorCAPath resolves the certificate the relay verifies the supervisor
// against. The default is where enrollment stores it; an explicit value covers
// a supervisor serving a real certificate from elsewhere.
func supervisorCAPath(supervisor map[string]any, keysDir string) string {
	if path, _ := supervisor["caPath"].(string); strings.TrimSpace(path) != "" {
		return strings.TrimSpace(path)
	}
	return filepath.Join(keysDir, supervisorCAName)
}

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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wf); err != nil {
		return nil, err
	}
	if err := wf.Validate(); err != nil {
		return nil, err
	}
	return &wf, nil
}
