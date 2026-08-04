// The git-agent adapter: whole-run relocation over the git-agent protocol.
// Execute snapshots the supervisor's dirty worktree, dispatches it to an
// enrolled agent's sidecar, and blocks until the two-tier vet loop produces a
// final verdict, which comes back as the run's response.
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
)

// GitAgent constructs the remote-execution adapter. Backend options carry the
// enrollment map and transport endpoints; nothing here is read from the spec
// except the work itself.
func GitAgent(cfg api.SandboxConfig) (api.Sandbox, error) {
	return &gitAgentSandbox{cfg: cfg}, nil
}

func init() {
	api.RegisterSandbox(api.SandboxGitAgent, GitAgent)
}

type gitAgentSandbox struct {
	cfg api.SandboxConfig
}

func (g *gitAgentSandbox) Kind() api.SandboxKind { return api.SandboxGitAgent }

func (g *gitAgentSandbox) Prepare(_ context.Context, _ *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}

func (g *gitAgentSandbox) Close() error { return nil }

// IsolatesWorkspace: the run happens in the agent's own worktree, so pairing
// with --worktree or a setup checkout must be refused, never doubled.
func (g *gitAgentSandbox) IsolatesWorkspace() bool { return true }

// ProvidesEgressProxy: the dispatch sandbox holds placeholders, not
// credentials; the sidecar proxy substitutes on the way out.
func (g *gitAgentSandbox) ProvidesEgressProxy() bool { return true }

func (g *gitAgentSandbox) Execute(ctx context.Context, spec api.Spec) (*api.Response, error) {
	if spec.Setup != nil && setup.Relocates(spec.Setup.Checkout) {
		return nil, fmt.Errorf("sandbox git-agent relocates the run; it cannot be combined with a setup checkout (register exactly one isolator)")
	}
	target, err := g.resolveTarget()
	if err != nil {
		return nil, err
	}
	repoDir := spec.Cwd()
	hooksJSON, _ := json.Marshal(map[string]any{
		"sidecar":    g.cfg.Options["hooks"],
		"supervisor": nil,
	})
	prompt := ""
	system := ""
	if spec.Prompt.User != "" {
		prompt = spec.Prompt.User
		system = spec.Prompt.System
	}
	dispatch, err := gitagent.Dispatch(ctx, gitagent.DispatchRequest{
		RepoDir:       repoDir,
		MailboxPath:   target.mailbox,
		Agent:         target.agent,
		SidecarURL:    target.url,
		SidecarHostFP: target.hostFingerprint,
		KeyPath:       target.keyPath,
		Relay:         target.relay,
		Policy:        target.policy,
		TaskPayload:   gitagent.TaskPayload{Prompt: prompt, System: system, Model: spec.Name},
		HooksJSON:     hooksJSON,
	})
	if err != nil {
		return nil, err
	}
	verdict, err := gitagent.AwaitOutcome(ctx, target.mailbox, dispatch.Task, target.waitTimeout)
	if err != nil {
		return nil, fmt.Errorf("task %s dispatched but not concluded: %w", dispatch.Task, err)
	}
	return gitAgentResponse(dispatch.Task, verdict), nil
}

func gitAgentResponse(task string, verdict *gitagent.TierVerdict) *api.Response {
	text := fmt.Sprintf("git-agent task %s attempt %d: %s", task, verdict.Attempt, verdict.Status)
	for _, f := range verdict.Findings {
		text += "\n" + f.Hook
		if f.Message != "" {
			text += ": " + f.Message
		}
	}
	resp := &api.Response{Text: text, StructuredData: verdict}
	for _, f := range verdict.Findings {
		if f.Hook == "integrate" && f.Path != "" {
			resp.Workspace = &api.Workspace{Branch: f.Path}
		}
	}
	return resp
}

type gitAgentTarget struct {
	agent           string
	url             string
	hostFingerprint string
	keyPath         string
	mailbox         string
	relay           gitagent.RelayMode
	policy          gitagent.Policy
	waitTimeout     time.Duration
}

// resolveTarget picks the enrolled agent — pinned by the spec's sandbox.agent,
// or the sole enrolled one — and assembles transport details from options.
func (g *gitAgentSandbox) resolveTarget() (*gitAgentTarget, error) {
	opts := g.cfg.Options
	agents, _ := opts["agents"].(map[string]any)
	name := g.cfg.Agent
	if name == "" {
		if len(agents) == 1 {
			for only := range agents {
				name = only
			}
		} else {
			return nil, fmt.Errorf("backend %q has %d enrolled agents; pin one with sandbox.agent", g.cfg.Name, len(agents))
		}
	}
	entry, _ := agents[name].(map[string]any)
	if entry == nil {
		return nil, fmt.Errorf("agent %q is not enrolled in backend %q", name, g.cfg.Name)
	}
	url, _ := entry["url"].(string)
	hostFP, _ := entry["hostFingerprint"].(string)
	if url == "" || hostFP == "" {
		return nil, fmt.Errorf("agent %q needs url and hostFingerprint recorded (its sidecar endpoint and host key)", name)
	}
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return nil, err
	}
	target := &gitAgentTarget{
		agent:           name,
		url:             url,
		hostFingerprint: hostFP,
		keyPath:         stringOption(opts, "key", filepath.Join(keysDir, "supervisor_ed25519")),
		mailbox:         stringOption(opts, "mailbox", filepath.Join(keysDir, "mailbox.git")),
		relay:           gitagent.RelayMode(stringOption(opts, "relay", string(gitagent.RelaySync))),
		waitTimeout:     time.Hour,
	}
	if raw, ok := opts["waitTimeout"].(string); ok {
		if d, err := time.ParseDuration(raw); err == nil {
			target.waitTimeout = d
		}
	}
	if g.cfg.Policy != nil {
		target.policy = gitagent.Policy{Paths: g.cfg.Policy.Paths, MaxAttempts: g.cfg.Policy.MaxAttempts}
	}
	return target, nil
}

func stringOption(opts map[string]any, key, fallback string) string {
	if v, ok := opts[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

// gitAgentKeysDir anchors key material and the default mailbox beside the
// captain config file, matching the CLI's layout.
func gitAgentKeysDir() (string, error) {
	path, err := captainconfig.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), ".captain", "sandbox"), nil
}
