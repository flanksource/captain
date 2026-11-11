// The git-agent adapter: whole-run relocation over the git-agent protocol.
// Execute snapshots the supervisor's dirty worktree, dispatches it to an
// enrolled agent's sidecar, and blocks until the two-tier vet loop produces a
// final outcome, returned as either the run response or a terminal worker error.
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/credproxy"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

// GitAgent constructs the remote-execution adapter. Backend options carry the
// enrollment map and transport endpoints; nothing here is read from the spec
// except the work itself.
func GitAgent(cfg api.SandboxConfig) (api.Sandbox, error) {
	if _, err := configuredWaitTimeout(cfg.Options); err != nil {
		return nil, err
	}
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

func (g *gitAgentSandbox) Execute(ctx context.Context, spec api.Spec, opts api.RemoteExecOptions) (*api.Response, error) {
	services, tools, err := taskTools(opts.CallerTools)
	if err != nil {
		return nil, err
	}
	sidecar, err := sidecarTools(spec.Tools, opts, services)
	if err != nil {
		return nil, err
	}
	if sidecar != nil {
		sidecar.Preferences, sidecar.Policy = spec.ToolPreferences, spec.ToolPolicy
	}
	if spec.Setup != nil && setup.Relocates(spec.Setup.Checkout) {
		return nil, fmt.Errorf("sandbox git-agent relocates the run; it cannot be combined with a setup checkout (register exactly one isolator)")
	}
	target, err := g.resolveTarget()
	if err != nil {
		return nil, err
	}
	repoDir := spec.Cwd()
	mailbox, err := gitagent.EnsureMailbox(ctx, target.mailboxRoot, repoDir)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	configPath, err := captainconfig.Path()
	if err != nil {
		return nil, err
	}
	if err := gitagent.InstallHookShims(mailbox.Path, exe, configPath, gitagent.RoleMailbox, g.cfg.Name); err != nil {
		return nil, err
	}
	hooksJSON, err := hookSetsJSON(g.cfg.Options)
	if err != nil {
		return nil, err
	}
	prompt := ""
	system := ""
	if spec.Prompt.User != "" {
		prompt = spec.Prompt.User
		system = spec.Prompt.System
	}
	timeout := strings.TrimSpace(spec.Budget.Timeout)
	if timeout == "" {
		timeout = target.waitTimeout.String()
	}
	transport := target.transport()
	dispatch, err := gitagent.Dispatch(ctx, gitagent.DispatchRequest{
		RepoDir:         repoDir,
		MailboxPath:     mailbox.Path,
		MailboxRoute:    mailbox.Route,
		Agent:           target.agent,
		SidecarURL:      transport.URL,
		SidecarHostFP:   transport.HostFingerprint,
		Token:           transport.Token,
		CAPath:          transport.CAPath,
		PinnedPublicKey: transport.PinnedPublicKey,
		KeyPath:         transport.KeyPath,
		Relay:           target.relay,
		Policy:          target.policy,
		// The resolved backend travels with the model: the agent must run the
		// runtime the supervisor selected, not re-resolve the name against its
		// own defaults and quietly pick a different one.
		TaskPayload: gitagent.TaskPayload{
			Prompt: prompt, System: system,
			Model: spec.Name, Mode: spec.Mode, Provider: specProviderName(spec), Effort: spec.Effort, Timeout: timeout,
			Tools: tools, Sidecar: sidecar, Requires: requirements(tools, sidecar),
		},
		HooksJSON: hooksJSON,
		Services:  services,
	})
	if err != nil {
		return nil, err
	}
	// The services serve every attempt of the task, so they are withdrawn
	// only once its outcome is in, whichever way the wait ends.
	defer func() {
		if err := gitagent.RemoveServices(mailbox.Path, dispatch.Task); err != nil {
			logger.Warnf("git-agent task %s: withdraw services: %v", dispatch.Task, err)
		}
	}()
	verdict, err := gitagent.AwaitOutcome(ctx, mailbox.Path, dispatch.Task, target.waitTimeout)
	if err != nil {
		return nil, fmt.Errorf("task %s dispatched but not concluded: %w", dispatch.Task, err)
	}
	response := gitAgentResponse(dispatch.Task, verdict)
	if verdict.Terminal && verdict.Status == gitagent.StatusError {
		return nil, errors.New(response.Text)
	}
	return response, nil
}

// taskTools publishes the run's caller-tool server to the task: its loopback
// address as the tools service, and its path and credential in the payload.
func taskTools(endpoint *api.CallerToolEndpoint) (gitagent.Services, *gitagent.TaskTools, error) {
	if endpoint == nil {
		return gitagent.Services{}, nil, nil
	}
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return nil, nil, fmt.Errorf("caller-tool endpoint %q is not a local http server the tunnel can reach", endpoint.URL)
	}
	services := gitagent.Services{gitagent.ToolsService: parsed.Host}
	if err := services.Validate(); err != nil {
		return nil, nil, fmt.Errorf("caller-tool endpoint: %w", err)
	}
	return services, &gitagent.TaskTools{Name: endpoint.Name, Path: parsed.RequestURI(), Headers: endpoint.Headers}, nil
}

// sidecarTools ships the declarations of the tools placed on the sidecar,
// adding each sidecar-proxied tool's credential release and each proxied
// tool's proxies to services. A grant travels as its destination and header
// names only: the declaration's values are the supervisor's to resolve, and a
// static one is a secret in clear text. A proxied tool's connections stay
// behind entirely — its kube proxy holds them.
func sidecarTools(declared api.InlineTools, opts api.RemoteExecOptions, services gitagent.Services) (*gitagent.TaskSidecarTools, error) {
	shipped := &gitagent.TaskSidecarTools{
		Tools:    api.InlineTools{},
		Releases: map[string]gitagent.TaskCredentialRelease{},
		Proxies:  map[string]gitagent.TaskToolProxies{},
	}
	for _, name := range declared.Names() {
		tool := declared[name]
		if tool.Placement != api.ToolPlacementSidecar {
			continue
		}
		tool.Grants = withoutGrantValues(tool.Grants)
		switch tool.Credentials {
		case api.ToolCredentialsSidecarProxied:
			release, ok := opts.CredentialReleases[name]
			if !ok {
				return nil, fmt.Errorf("tools.%s is sidecar-proxied, but the run serves no credential release for it", name)
			}
			service := "creds/" + name
			services[service] = release.Addr
			shipped.Releases[name] = gitagent.TaskCredentialRelease{Service: service, Token: release.Token}
		case api.ToolCredentialsProxied:
			endpoints, ok := opts.ToolProxies[name]
			if !ok {
				return nil, fmt.Errorf("tools.%s is proxied, but the run serves no proxies for it", name)
			}
			proxies, err := publishToolProxies(name, endpoints, services)
			if err != nil {
				return nil, err
			}
			tool.Connections = connection.ExecConnections{}
			shipped.Proxies[name] = proxies
		}
		shipped.Tools[name] = tool
	}
	if len(shipped.Tools) == 0 {
		return nil, nil
	}
	return shipped, services.Validate()
}

// publishToolProxies adds a proxied tool's egress and kube proxies to services
// as egress/<tool> and kube/<tool>.
func publishToolProxies(name string, endpoints api.ToolProxyEndpoints, services gitagent.Services) (gitagent.TaskToolProxies, error) {
	proxies := gitagent.TaskToolProxies{EgressSecret: endpoints.EgressSecret, CAPEM: endpoints.CAPEM, KubeToken: endpoints.KubeToken}
	if endpoints.Egress != "" {
		proxies.EgressService = "egress/" + name
		services[proxies.EgressService] = endpoints.Egress
	}
	if endpoints.KubeURL != "" {
		parsed, err := url.Parse(endpoints.KubeURL)
		if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
			return proxies, fmt.Errorf("tools.%s: kube proxy %q is not a local http server the tunnel can reach", name, endpoints.KubeURL)
		}
		proxies.KubeService = "kube/" + name
		services[proxies.KubeService] = parsed.Host
	}
	if proxies.EgressService == "" && proxies.KubeService == "" {
		return proxies, fmt.Errorf("tools.%s is proxied, but declares neither grants nor a kubernetes connection to proxy", name)
	}
	return proxies, nil
}

func withoutGrantValues(grants []credproxy.Grant) []credproxy.Grant {
	stripped := make([]credproxy.Grant, len(grants))
	for i, grant := range grants {
		stripped[i] = grant
		stripped[i].Headers = make([]credproxy.HeaderGrant, len(grant.Headers))
		for j, header := range grant.Headers {
			stripped[i].Headers[j] = credproxy.HeaderGrant{Name: header.Name}
		}
	}
	return stripped
}

func requirements(tools *gitagent.TaskTools, sidecar *gitagent.TaskSidecarTools) []string {
	var required []string
	if tools != nil {
		required = append(required, gitagent.RequiresTools)
	}
	if sidecar != nil {
		required = append(required, gitagent.RequiresSidecarTools)
	}
	return required
}

// specProviderName is the resolved provider's key, or "" when the spec named a
// model no provider claims — the runner re-derives it from the model name then.
func specProviderName(spec api.Spec) string {
	if spec.Provider == nil {
		return ""
	}
	return spec.Provider.Name
}

// hookSetsJSON preserves the sidecar/supervisor split declared by the backend
// and validates it against the protocol payload before dispatch.
func hookSetsJSON(options map[string]any) ([]byte, error) {
	value := any(map[string]any{})
	if configured, ok := options["hooks"]; ok {
		value = configured
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode git-agent hooks: %w", err)
	}
	if _, err := gitagent.DecodeHookSets(data); err != nil {
		return nil, err
	}
	return data, nil
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
			resp.Workspace = &api.Workspace{Worktree: &api.WorktreeState{Branch: f.Path}}
		}
	}
	return resp
}

type gitAgentTarget struct {
	agent           string
	url             string
	hostFingerprint string
	// token authenticates this supervisor to an https agent, and is empty for an
	// ssh one, which authenticates by key instead.
	token           text.SensitiveString
	caPath          string
	pinnedPublicKey string
	keyPath         string
	mailboxRoot     string
	relay           gitagent.RelayMode
	policy          gitagent.Policy
	waitTimeout     time.Duration
}

// transport is the shared trust and authentication boundary for Git dispatch
// and HTTPS sidecar control requests.
func (target gitAgentTarget) transport() gitagent.TransportTarget {
	return gitagent.TransportTarget{
		URL: target.url, KeyPath: target.keyPath, HostFingerprint: target.hostFingerprint,
		Token: target.token, CAPath: target.caPath, PinnedPublicKey: target.pinnedPublicKey,
	}
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
	if url == "" {
		return nil, fmt.Errorf(
			"agent %q has no endpoint recorded: it enrolled before advertising one. "+
				"Re-enroll it (captain sandbox git-agent add %s) so its serve reports its URL",
			name, name)
	}
	hostFP, token, err := dispatchCredentials(name, url, entry)
	if err != nil {
		return nil, err
	}
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return nil, err
	}
	target := &gitAgentTarget{
		agent:           name,
		url:             url,
		hostFingerprint: hostFP,
		token:           token,
		caPath:          stringOption(entry, "caPath", ""),
		pinnedPublicKey: stringOption(entry, "pinnedPubkey", ""),
		keyPath:         stringOption(opts, "key", filepath.Join(keysDir, dispatchKeyFile)),
		// The long-running endpoint serves this root; each dispatch derives a
		// repository-specific mailbox beneath it from the request working tree.
		mailboxRoot: stringOption(opts, "mailboxRoot", filepath.Join(keysDir, servedReposDir)),
		relay:       gitagent.RelayMode(stringOption(opts, "relay", string(gitagent.RelaySync))),
		waitTimeout: WaitTimeout(opts),
	}
	if g.cfg.Dispatch != nil {
		target.policy = gitagent.Policy{Paths: g.cfg.Dispatch.Paths, MaxAttempts: g.cfg.Dispatch.MaxAttempts}
	}
	return target, nil
}

// dispatchCredentials resolves how this supervisor authenticates to an agent,
// which the agent's own URL scheme decides.
func dispatchCredentials(name, endpoint string, entry map[string]any) (string, text.SensitiveString, error) {
	switch scheme := gitagent.EndpointScheme(endpoint); scheme {
	case "ssh":
		hostFP, _ := entry["hostFingerprint"].(string)
		if hostFP == "" {
			return "", "", fmt.Errorf(
				"agent %q has no host key recorded: it enrolled before advertising one. "+
					"Re-enroll it (captain sandbox git-agent add %s) so its serve reports its URL and host key",
				name, name)
		}
		return hostFP, "", nil
	case "https":
		path, _ := entry["tokenPath"].(string)
		if path == "" {
			return "", "", fmt.Errorf(
				"agent %q is reached over https but issued this supervisor no dispatch token, so there is "+
					"nothing to authenticate with; re-enroll it (captain sandbox git-agent add %s)", name, name)
		}
		// Read at the moment of use, exactly as the relay does, so a re-enrolled
		// agent's rotated token takes effect without restarting the supervisor.
		token, err := gitagent.ReadTokenFile(path)
		if err != nil {
			return "", "", fmt.Errorf("agent %q: %w", name, err)
		}
		return "", token, nil
	default:
		return "", "", fmt.Errorf(
			"agent %q advertised %s, whose scheme %q is not a transport captain speaks; want ssh:// or https://",
			name, endpoint, scheme)
	}
}

func stringOption(opts map[string]any, key, fallback string) string {
	if v, ok := opts[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

// The on-disk layout shared with the CLI (pkg/cli/gitagent.go). Duplicated as
// constants rather than imported because pkg/cli imports this package.
const (
	dispatchKeyFile = "supervisor_ed25519"
	servedReposDir  = "repos"
)

// DefaultWaitTimeout bounds how long a dispatch waits for its verdict. A
// relocating sandbox blocks on a remote agent doing real work, so this is
// sized for that rather than for a model call.
const DefaultWaitTimeout = time.Hour

// WaitTimeout reads the backend's waitTimeout option, falling back to
// DefaultWaitTimeout. Exported so the run path can size its own deadline to
// match: a shorter outer timeout would kill the dispatch mid-flight and report
// a failure for work that is still running.
func WaitTimeout(options map[string]any) time.Duration {
	timeout, err := configuredWaitTimeout(options)
	if err != nil {
		return DefaultWaitTimeout
	}
	return timeout
}

func configuredWaitTimeout(options map[string]any) (time.Duration, error) {
	value, exists := options["waitTimeout"]
	if !exists {
		return DefaultWaitTimeout, nil
	}
	raw, ok := value.(string)
	if !ok {
		return 0, fmt.Errorf("waitTimeout must be a duration string, got %T", value)
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("waitTimeout %q: %w", raw, err)
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("waitTimeout must be positive, got %q", raw)
	}
	return timeout, nil
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
