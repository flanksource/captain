package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

// gitAgentDirectory satisfies the server's authorization source.
var _ gitagent.AgentDirectory = gitAgentDirectory{}

// gitAgentDirectory implements gitagent.AgentDirectory over two sources. The
// agent roster lives in the sandbox backend's options block in ~/.captain.yaml,
// because it is dispatch targeting data — an endpoint and a host key, not a
// credential. Tokens live in the database, password-hashed.
//
// Every read of either goes to the live source, so a revocation takes effect
// for the next connection (R8.5), and every config mutation goes through the
// flocked captainconfig.Update (A3.4).
type gitAgentDirectory struct {
	backend string
	// ctx and db back AdmitToken. A directory built without them refuses
	// enrollment rather than falling back to an unauthenticated one.
	ctx context.Context
	db  *database.DB
}

// gitAgentDirectoryFor builds the directory a receiver authorizes against.
//
// Only a mailbox enrolls agents, and only a mailbox needs the token store —
// which is also the only role that runs on the host holding it. A sidecar gets
// no database rather than one it would never read.
func gitAgentDirectoryFor(ctx context.Context, role gitagent.ReceiverRole, backend string) (gitAgentDirectory, error) {
	directory := gitAgentDirectory{backend: backend, ctx: ctx}
	if role != gitagent.RoleMailbox {
		return directory, nil
	}
	db, err := captainServeDB(ctx)
	if err != nil {
		return gitAgentDirectory{}, fmt.Errorf("a mailbox verifies captain tokens against the database: %w", err)
	}
	directory.db = db
	return directory, nil
}

func (d gitAgentDirectory) AgentByFingerprint(fingerprint string) (string, bool) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return "", false
	}
	backend, ok := cfg.Sandbox.Backends[d.backend]
	if !ok {
		return "", false
	}
	agents, _ := backend.Options["agents"].(map[string]any)
	for name, v := range agents {
		if entry, ok := v.(map[string]any); ok && entry["fingerprint"] == fingerprint {
			return name, true
		}
	}
	return "", false
}

// AdmitToken verifies a presented captain token and resolves the agent it
// speaks for.
//
// The token is not spent. A sidecar that restarts presents the same one, which
// is the whole point of the change: the single-use token it replaces made every
// restart of a long-lived workload a crash loop, and every code path where
// enrollment could re-run needed a guard against it.
func (d gitAgentDirectory) AdmitToken(token, requested string) (string, error) {
	if d.db == nil {
		return "", fmt.Errorf("this endpoint cannot verify captain tokens: it was started without a database")
	}
	ctx := d.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	record, err := captaintoken.NewVerifier(d.db.LookupAPIToken).
		VerifyScope(ctx, token, captaintoken.ScopeGit)
	if err != nil {
		return "", enrollmentRefusal(err)
	}
	name, err := d.db.AdmitAPITokenAgent(ctx, record.ID, requested)
	if err != nil {
		return "", enrollmentRefusal(err)
	}
	if err := d.db.TouchAPIToken(ctx, record.ID); err != nil {
		log.Warnf("record captain token use: %v", err)
	}
	return name, nil
}

// enrollmentRefusal turns a verification failure into something an operator can
// act on. An unknown id and a wrong secret share one answer, but a revoked,
// expired or exhausted credential is a real one whose holder benefits from
// knowing which — and each calls for a different fix.
func enrollmentRefusal(err error) error {
	switch {
	case errors.Is(err, captaintoken.ErrRevoked):
		return fmt.Errorf("this captain token has been revoked; mint a new one with `captain token create`")
	case errors.Is(err, captaintoken.ErrExpired):
		return fmt.Errorf("this captain token has expired; mint a new one with `captain token create`")
	case errors.Is(err, captaintoken.ErrScope):
		return fmt.Errorf("this captain token does not carry the %s scope, so it cannot enroll an agent", captaintoken.ScopeGit)
	case errors.Is(err, database.ErrAPITokenPoolFull):
		return err
	case errors.Is(err, captaintoken.ErrUnknown), errors.Is(err, captaintoken.ErrMalformed):
		return fmt.Errorf("captain token is not recognized")
	default:
		// A store outage is not a rejection. Saying so keeps an operator from
		// hunting a phantom credential problem through a database failure.
		return fmt.Errorf("cannot verify captain tokens right now: %w", err)
	}
}

// RecordAgent stores everything a dispatch to this agent needs: its client
// key, its endpoint, and the host key to pin when pushing there. Recording
// only the key would leave an enrollment that looks complete but cannot be
// dispatched to.
func (d gitAgentDirectory) RecordAgent(e gitagent.AgentEnrollment) error {
	if e.URL == "" {
		return fmt.Errorf(
			"agent %q advertised no endpoint; rerun its serve with --advertise ssh://host:port or "+
				"--advertise https://host/git/%s", e.Name, SidecarRepoName)
	}
	credential, err := recordDispatchCredential(e)
	if err != nil {
		return err
	}
	return captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, d.backend)
		if err != nil {
			return err
		}
		agents, _ := backend.Options["agents"].(map[string]any)
		if agents == nil {
			agents = map[string]any{}
		}
		entry := map[string]any{
			"fingerprint": e.Fingerprint,
			"url":         e.URL,
			"addedAt":     time.Now().UTC().Format(time.RFC3339),
		}
		// Only the credential this transport uses is written, so re-enrolling an
		// agent across transports cannot leave the other one's key beside it and
		// make the entry look like it authenticates two ways.
		maps.Copy(entry, credential)
		agents[e.Name] = entry
		backend.Options["agents"] = agents
		cfg.Sandbox.Backends[d.backend] = backend
		return nil
	})
}

// recordDispatchCredential resolves what the supervisor must present to this
// agent when it dispatches, which is decided entirely by how the agent is
// reached: an ssh endpoint is pinned by host key, an https one authenticates
// with the bearer token the agent minted for exactly this purpose.
//
// There is deliberately no cross-transport leniency. An ssh agent with no host
// key and an https agent with no token are both endpoints the supervisor could
// reach but not authenticate to, and recording either would produce a roster
// that looks complete and fails at the first dispatch.
func recordDispatchCredential(e gitagent.AgentEnrollment) (map[string]any, error) {
	switch scheme := gitagent.EndpointScheme(e.URL); scheme {
	case "ssh":
		if strings.TrimSpace(e.HostFingerprint) == "" {
			return nil, fmt.Errorf("agent %q advertised no host key fingerprint; its dispatch could not be verified", e.Name)
		}
		return map[string]any{"hostFingerprint": strings.TrimSpace(e.HostFingerprint)}, nil
	case "https":
		if strings.TrimSpace(e.DispatchToken) == "" {
			return nil, fmt.Errorf(
				"agent %q advertised the https endpoint %s but issued no dispatch token, so this supervisor "+
					"has no way to authenticate to it; rerun its serve with --transport https", e.Name, e.URL)
		}
		path, err := writeDispatchTokenFile(e.Name, e.DispatchToken)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tokenPath": path}, nil
	default:
		return nil, fmt.Errorf(
			"agent %q advertised %s, whose scheme %q is not a transport captain speaks; want ssh:// or https://",
			e.Name, e.URL, scheme)
	}
}

// dispatchTokensDir holds one file per agent this supervisor dispatches to over
// https.
//
// The config records the path, never the value. That is the rule the relay
// already follows in the other direction ("a path rather than the credential
// itself, exactly as KeyPath is"), and ~/.captain.yaml has no guaranteed mode,
// is read by hook shims running as whoever pushed, and is echoed in dry-run
// output. A per-agent file also means a leak of one is a leak of one.
//
// A database column was considered and rejected: the token store holds argon2
// hashes of credentials captain ISSUES and is documented as never letting them
// leave the package, while this is a credential captain must be able to
// PRESENT. Storing it there would mean recoverable plaintext in Postgres, a
// migration, and wiring a *database.DB into pkg/sandbox/adapter, which cannot
// import this package.
const dispatchTokensDir = "dispatch-tokens"

func writeDispatchTokenFile(agent, token string) (string, error) {
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(keysDir, dispatchTokensDir, agent+".token")
	if err := gitagent.WriteTokenFile(path, text.NewSensitiveString(token)); err != nil {
		return "", fmt.Errorf("store the dispatch token for agent %q: %w", agent, err)
	}
	return path, nil
}

// removeDispatchTokenFile drops an agent's credential when its roster entry
// goes, so revoking an agent does not leave a live way in on disk.
func removeDispatchTokenFile(agent string) error {
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(keysDir, dispatchTokensDir, agent+".token"))
	if errors.Is(err, os.ErrNotExist) {
		return nil // an ssh agent never had one
	}
	return err
}

// ensureGitAgentBackend returns the named backend, creating a git-agent one
// (with an initialized Options map) when absent so `add` works on a fresh
// config.
func ensureGitAgentBackend(cfg *captainconfig.Config, name string) (captainconfig.SandboxBackend, error) {
	if cfg.Sandbox.Backends == nil {
		cfg.Sandbox.Backends = map[string]captainconfig.SandboxBackend{}
	}
	backend, ok := cfg.Sandbox.Backends[name]
	if !ok {
		backend = captainconfig.SandboxBackend{Kind: string(registry.SandboxGitAgent)}
	} else if backend.Kind != "" && backend.Kind != string(registry.SandboxGitAgent) {
		return backend, fmt.Errorf("backend %q is kind %q, not %s", name, backend.Kind, registry.SandboxGitAgent)
	} else if backend.Kind == "" {
		backend.Kind = string(registry.SandboxGitAgent)
	}
	if backend.Options == nil {
		backend.Options = map[string]any{}
	}
	cfg.Sandbox.Backends[name] = backend
	return backend, nil
}
