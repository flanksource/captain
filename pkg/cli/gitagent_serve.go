package cli

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/text"
	gossh "golang.org/x/crypto/ssh"
)

// RunGitAgentServe runs the receive endpoint on this host, optionally
// enrolling with a supervisor first. The agent keypair is generated locally
// on first start and its private half never leaves this machine (R8.2).
func RunGitAgentServe(ctx context.Context, opts GitAgentServeOptions) (any, error) {
	role := gitagent.ReceiverRole(opts.Role)
	if role != gitagent.RoleSidecar && role != gitagent.RoleMailbox {
		return nil, fmt.Errorf("--role must be %q or %q", gitagent.RoleSidecar, gitagent.RoleMailbox)
	}
	transport, err := validateServeTransport(opts, role)
	if err != nil {
		return nil, err
	}
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return nil, err
	}
	root := opts.Root
	if root == "" {
		if root, err = gitAgentServedRoot(); err != nil {
			return nil, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	token, err := opts.enrollmentToken()
	if err != nil {
		return nil, err
	}
	if !token.IsEmpty() {
		agent, err := reusableEnrollment(opts, transport, token, keysDir)
		if err != nil {
			return nil, err
		}
		if agent == "" {
			if err := joinSupervisor(ctx, opts, transport, token, keysDir); err != nil {
				return nil, err
			}
		} else {
			clicky.Printf("resuming enrollment as %s\n", agent)
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	// Reclaim worktrees orphaned by a crashed hook (R10.3).
	gitagent.PruneWorktrees(ctx, root)
	if err := ensureServedRepos(ctx, root, role, opts); err != nil {
		return nil, err
	}
	// An https sidecar has no ssh host key: nothing presents it, and generating
	// one would leave a private key on the box that runs agent-authored code for
	// a listener that does not exist.
	var hostKey gossh.Signer
	var hostFP string
	if transport == transportSSH {
		if hostKey, hostFP, err = gitagent.EnsureKeyPair(filepath.Join(keysDir, hostKeyName)); err != nil {
			return nil, err
		}
	}
	// After the host key exists: the record publishes this endpoint's identity,
	// which is that key's fingerprint.
	if err := recordServedEndpoint(opts, role, transport, root, hostFP); err != nil {
		return nil, err
	}
	if transport == transportHTTPS {
		startSidecarBackground(ctx, root)
		return nil, serveSidecarHTTPS(ctx, sidecarHTTPSPlan{
			listen: opts.Listen, root: root, keysDir: keysDir, advertise: opts.Advertise, backend: opts.Backend,
			certPath: opts.TLSCert, keyPath: opts.TLSKey,
		})
	}
	offer, err := enrollmentOffer(role, keysDir)
	if err != nil {
		return nil, err
	}
	directory, err := gitAgentDirectoryFor(ctx, role, opts.Backend)
	if err != nil {
		return nil, err
	}
	server, err := gitagent.NewServer(gitagent.ServerConfig{
		Listen:        opts.Listen,
		Root:          root,
		Role:          role,
		HostKey:       hostKey,
		Directory:     directory,
		Offer:         offer,
		AgentRepoPath: SidecarRepoName,
	})
	if err != nil {
		return nil, err
	}
	clicky.Printf("captain git-agent %s serving %s on %s\n", role, root, opts.Listen)
	clicky.Printf("  host key: %s\n", hostFP)
	if role == gitagent.RoleMailbox {
		clicky.Printf("  enroll an agent with: captain sandbox git-agent add <name> --endpoint ssh://<this-host>:<port>\n")
	} else {
		startSidecarBackground(ctx, root)
	}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.ListenAndServe(); err != nil && ctx.Err() == nil {
		return nil, err
	}
	return nil, nil
}

// startSidecarBackground launches the work a sidecar does alongside its
// listener, whichever transport that listener speaks: streaming the agent's task
// log, and materializing the CLI logins the supervisor publishes. It returns
// once both are running.
func startSidecarBackground(ctx context.Context, root string) {
	monitor := newAgentTaskLogMonitor(filepath.Join(root, SidecarRepoName), os.Stdout, os.Stderr, log.Infof)
	if err := monitor.prime(); err != nil {
		log.Warnf("git-agent task log monitor: %v", err)
	}
	go monitor.run(ctx)

	// Only the sidecar runs coding agents, so only the sidecar needs the
	// agent CLI logins the supervisor publishes.
	if home, err := os.UserHomeDir(); err != nil {
		log.Warnf("git-agent credential materializer: resolve home directory: %v", err)
	} else if credentials := newCredentialMaterializer(home); credentials.mounted() {
		clicky.Printf("  credentials: %s\n", deploy.CredentialsMountPath)
		go credentials.run(ctx)
	}
}

// validateServeTransport resolves which protocol the receive endpoint speaks,
// refusing every combination that would produce a listener the supervisor
// cannot reach or a roster entry that names one this process does not serve.
func validateServeTransport(opts GitAgentServeOptions, role gitagent.ReceiverRole) (mailboxTransport, error) {
	transport, err := parseMailboxTransport(opts.Transport)
	if err != nil {
		return "", err
	}
	if transport == "" {
		transport = transportSSH
	}
	if transport == transportSSH {
		return transport, nil
	}
	// A mailbox over https is `captain serve`'s handler, which also needs the
	// token store and the web UI. Keeping this command to one role means the
	// https path has exactly one shape.
	if role != gitagent.RoleSidecar {
		return "", fmt.Errorf(
			"a mailbox over https is hosted by `captain serve --tls`, not by this command; run " +
				"`captain serve --host 0.0.0.0 --tls --tls-host <address agents dial>`")
	}
	// The supervisor derives an ssh:// URL from the connection when an agent
	// advertises nothing (pkg/gitagent/server.go), so an https sidecar that said
	// nothing would be recorded at an endpoint it does not serve.
	advertise, err := advertiseURL(opts.Advertise)
	if err != nil {
		return "", err
	}
	if advertise == "" {
		return "", fmt.Errorf(
			"--transport https needs --advertise https://<host>/git/%s; this agent's endpoint cannot be "+
				"inferred from the connection, and the supervisor would record an ssh:// URL nothing serves",
			SidecarRepoName)
	}
	if scheme := gitagent.EndpointScheme(advertise); scheme != "https" {
		return "", fmt.Errorf(
			"--transport https serves an https endpoint but --advertise %s is %s://; the supervisor would "+
				"dispatch to a protocol this agent does not speak", opts.Advertise, scheme)
	}
	return transport, nil
}

// enrollmentOffer is what this endpoint hands a joining agent. Only a mailbox
// has a dispatch key for the agent to authorize; task-specific mailbox routes
// arrive later in authenticated dispatch envelopes.
func enrollmentOffer(role gitagent.ReceiverRole, keysDir string) (gitagent.EnrollmentOffer, error) {
	if role != gitagent.RoleMailbox {
		return gitagent.EnrollmentOffer{}, nil
	}
	_, dispatchFP, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, dispatchKeyName))
	if err != nil {
		return gitagent.EnrollmentOffer{}, err
	}
	return gitagent.EnrollmentOffer{DispatchKey: dispatchFP}, nil
}

// enrolledAgentName reports the name this host was given by an earlier
// enrollment, empty when it has none.
//
// Re-presenting it is what lets a pool member reclaim its slot across a restart
// instead of consuming another and eventually exhausting the pool. A read
// failure reports "no name", which costs a slot but never claims one that
// belongs to a sibling.
func enrolledAgentName(backendName string) string {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return ""
	}
	backend, ok := cfg.Sandbox.Backends[backendName]
	if !ok {
		return ""
	}
	supervisor, _ := backend.Options["supervisor"].(map[string]any)
	name, _ := supervisor["agent"].(string)
	return strings.TrimSpace(name)
}

// joinSupervisor performs the enrollment exchange and records both directions
// of trust: the supervisor's dispatch key is authorized locally, and its base
// endpoint is retained for task-specific result relays.
func joinSupervisor(
	ctx context.Context, opts GitAgentServeOptions, transport mailboxTransport,
	token text.SensitiveString, keysDir string,
) error {
	if opts.Supervisor == "" {
		return fmt.Errorf("a captain token requires --supervisor ssh://host:port or https://host:port")
	}
	signer, fp, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, agentKeyName))
	if err != nil {
		return err
	}
	// Whichever credential this endpoint will be reached by has to exist before
	// it is advertised, and it must be committed to disk before it is named: a
	// supervisor holding a credential this host has not persisted would fail at
	// the first dispatch instead of here.
	hostFP, dispatchToken, err := issueReceiveCredential(transport, keysDir)
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(opts.Listen)
	if err != nil {
		return fmt.Errorf("--listen %q must be [host]:port: %w", opts.Listen, err)
	}
	// Verify the local half can be persisted before enrolling. The exchange
	// cannot be transactional across hosts, so this catches path, permission
	// and backend-kind failures up front rather than after the supervisor has
	// already recorded this agent.
	if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		_, err := ensureGitAgentBackend(cfg, opts.Backend)
		return err
	}); err != nil {
		return fmt.Errorf("prepare local enrollment config: %w", err)
	}
	advertise, err := advertiseURL(opts.Advertise)
	if err != nil {
		return err
	}
	resp, err := gitagent.Enroll(ctx, opts.Supervisor, token.Value(), opts.HostFingerprint, signer, gitagent.EnrollRequest{
		Agent:           enrolledAgentName(opts.Backend),
		AdvertiseURL:    advertise,
		ListenPort:      port,
		HostFingerprint: hostFP,
		DispatchToken:   dispatchToken.Value(),
	})
	if err != nil {
		return err
	}
	// Kept for the relay, which presents it to the mailbox on every push. The
	// enrollment token and the relay credential are the same durable token —
	// that is what makes a restart free.
	tokenPath := filepath.Join(keysDir, gitagent.TokenFileName)
	if err := gitagent.WriteTokenFile(tokenPath, token); err != nil {
		return err
	}
	// The supervisor's certificate arrives over the exchange this agent already
	// pinned, which is the only channel where receiving it proves anything.
	caPath := filepath.Join(keysDir, supervisorCAName)
	if resp.CACertificate != "" {
		if err := os.WriteFile(caPath, []byte(resp.CACertificate), 0o644); err != nil { //nolint:gosec // a certificate is public
			return fmt.Errorf("store the supervisor's certificate: %w", err)
		}
	} else {
		caPath = ""
	}
	err = captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, opts.Backend)
		if err != nil {
			return err
		}
		// The task supplies its repository-specific mailbox route; enrollment
		// records only the stable supervisor endpoint and host identity, plus
		// the name this agent was given so a restart reclaims it.
		backend.Options["supervisor"] = map[string]any{
			"url":             strings.TrimSuffix(opts.Supervisor, "/"),
			"hostFingerprint": strings.TrimSpace(opts.HostFingerprint),
			"agent":           resp.Agent,
			"tokenPath":       tokenPath,
			"caPath":          caPath,
			"pinnedPubkey":    resp.PinnedPublicKey,
		}
		// Authorize the supervisor's dispatch key so its push is accepted
		// here — the direction a one-way enrollment leaves broken.
		//
		// Only over ssh. An https endpoint authenticates the supervisor by the
		// token it just issued, so recording a key here would authorize a
		// listener that does not exist: a dead credential rather than a spare.
		if transport == transportSSH {
			agents, _ := backend.Options["agents"].(map[string]any)
			if agents == nil {
				agents = map[string]any{}
			}
			agents[supervisorAgentID] = map[string]any{"fingerprint": resp.DispatchKey}
			backend.Options["agents"] = agents
		}
		cfg.Sandbox.Backends[opts.Backend] = backend
		return nil
	})
	if err != nil {
		return fmt.Errorf("supervisor enrolled agent %q, but the local relay config could not be saved; fix the config and rerun — the token is durable, so the same one still works: %w", resp.Agent, err)
	}
	clicky.Printf("enrolled as %s\n", resp.Agent)
	clicky.Printf("  this agent's key:   %s\n", fp)
	clicky.Printf("  relays to:          %s/<task-mailbox>\n", strings.TrimSuffix(opts.Supervisor, "/"))
	if transport == transportSSH {
		clicky.Printf("  this endpoint's host key: %s\n", hostFP)
		clicky.Printf("  authorized supervisor key: %s\n", resp.DispatchKey)
		return nil
	}
	// The id, never the secret: it is enough to correlate a refused push with
	// the credential in play, and it is not a credential itself.
	if presented, err := captaintoken.Parse(dispatchToken.Value()); err == nil {
		clicky.Printf("  issued the supervisor a dispatch token: %s\n", presented.ID)
	}
	return nil
}

// issueReceiveCredential creates whatever the supervisor will authenticate to
// this endpoint with, and returns it for the enrollment request.
//
// Exactly one of the two is ever produced, because exactly one listener is ever
// served. Over https the verifier is on disk before this returns, so the secret
// is only ever named after this host can already honour it.
func issueReceiveCredential(transport mailboxTransport, keysDir string) (string, text.SensitiveString, error) {
	if transport == transportSSH {
		_, hostFP, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, hostKeyName))
		return hostFP, "", err
	}
	token, err := gitagent.MintDispatchCredential(filepath.Join(keysDir, gitagent.DispatchCredentialName))
	return "", token, err
}

// advertiseURL normalizes an endpoint into the full push URL the supervisor
// dispatches to, appending the sidecar repository when only an origin was given.
//
// The scheme decides where the repository goes: ssh serves it directly under the
// served root, https serves it under GitHTTPPrefix. It is parsed rather than
// matched on string prefixes because the earlier form tested the remainder after
// trimming "ssh://", which for an https URL is a no-op that leaves the scheme's
// own slashes in place — so no repository was ever appended to an https origin,
// and splitGitPath refused every push to it with a 403.
func advertiseURL(raw string) (string, error) {
	advertise := strings.TrimSpace(raw)
	if advertise == "" {
		return "", nil
	}
	if !strings.Contains(advertise, "://") {
		advertise = "ssh://" + advertise // the form written before HTTPS existed
	}
	parsed, err := url.Parse(advertise)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("--advertise %q must be ssh://[user@]host[:port] or https://host[:port]", raw)
	}
	// Before the path, so an unsupported scheme is refused even when it already
	// names a repository and would otherwise be taken as a full URL.
	if parsed.Scheme != "ssh" && parsed.Scheme != "https" {
		return "", fmt.Errorf("--advertise %q uses scheme %q; captain speaks ssh:// and https://", raw, parsed.Scheme)
	}
	// Rebuilt from the parsed parts rather than trimmed as a string: url.Parse
	// splits userinfo off the host, and string surgery on the raw value mangles
	// inputs like "ssh://" into a host named "ssh:".
	origin := parsed.Scheme + "://" + parsed.Host
	if parsed.User != nil {
		origin = parsed.Scheme + "://" + parsed.User.String() + "@" + parsed.Host
	}
	if repo := strings.Trim(parsed.Path, "/"); repo != "" {
		return origin + "/" + repo, nil // already a full URL, taken as given
	}
	if parsed.Scheme == "https" {
		return gitagent.HTTPSRepoURL(origin, SidecarRepoName)
	}
	return origin + "/" + SidecarRepoName, nil
}

// ensureServedRepos creates the role's repository and (re-)installs the hook
// shims on every repo under root, so an upgraded captain binary repoints the
// shims at itself.
func ensureServedRepos(ctx context.Context, root string, role gitagent.ReceiverRole, opts GitAgentServeOptions) error {
	switch role {
	case gitagent.RoleSidecar:
		if err := gitagent.InitSidecar(ctx, filepath.Join(root, SidecarRepoName)); err != nil {
			return err
		}
	case gitagent.RoleMailbox:
		if err := os.MkdirAll(filepath.Join(root, gitagent.MailboxesDir), 0o755); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Bake the config path into the shims: a hook inherits the pusher's
	// environment, and a co-located agent pushes from its own shell.
	configPath, err := captainconfig.Path()
	if err != nil {
		return err
	}
	var repos []string
	if role == gitagent.RoleSidecar {
		repos = append(repos, filepath.Join(root, SidecarRepoName))
	} else {
		entries, err := os.ReadDir(filepath.Join(root, gitagent.MailboxesDir))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				repos = append(repos, filepath.Join(root, gitagent.MailboxesDir, e.Name()))
			}
		}
	}
	for _, repo := range repos {
		if _, err := os.Stat(filepath.Join(repo, "HEAD")); err != nil {
			continue
		}
		if err := gitagent.InstallHookShims(repo, exe, configPath, role, opts.Backend); err != nil {
			return err
		}
	}
	return nil
}

// recordServedEndpoint publishes what this long-running SSH endpoint serves, so
// other captain processes on the host can find it without being told.
//
// For a mailbox it records the root (dispatch creates repository-specific
// mailboxes under it) plus the listen address and host-key fingerprint, which
// are what `git-agent deploy` needs to prove the mailbox it is about to enroll
// an agent against is live and is this host's. Nothing else writes those: the
// backend's `url` option is read in two places and produced by hand.
//
// A sidecar records nothing, but clears an ssh mailbox record that names the
// same listen address — see clearMailboxRecord for why.
func recordServedEndpoint(
	opts GitAgentServeOptions, role gitagent.ReceiverRole, transport mailboxTransport,
	root, hostFingerprint string,
) error {
	return captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, opts.Backend)
		if err != nil {
			return err
		}
		if role == gitagent.RoleMailbox {
			// mailboxRoot stays a top-level key: gitagent.ServedRootFor reads it
			// directly, and moving it would break dispatch mid-upgrade.
			backend.Options["mailboxRoot"] = root
			setMailboxRecord(backend.Options, mailboxRecord{
				Transport: transportSSH,
				Root:      root,
				Listen:    opts.Listen,
				Identity:  hostFingerprint,
				Encrypted: true,
			})
		} else {
			// A sidecar now holds this address, so any mailbox record claiming it
			// over the protocol the sidecar serves is stale — and over ssh no probe
			// could tell the two apart, because both present the same host key.
			clearMailboxRecord(backend.Options, transport, opts.Listen)
		}
		cfg.Sandbox.Backends[opts.Backend] = backend
		return nil
	})
}
