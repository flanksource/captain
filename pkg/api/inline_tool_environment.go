package api

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/shell"
)

// ToolCredentials is how an inline tool's credentials reach its script.
type ToolCredentials string

const (
	// ToolCredentialsNone gives the script no credentials: neither its own nor
	// the run's.
	ToolCredentialsNone ToolCredentials = "none"
	// ToolCredentialsDirect hands the script the real credentials. On the host
	// the tool is then the proxy: it performs the credentialed operation and the
	// agent sees only its result.
	ToolCredentialsDirect ToolCredentials = "direct"
	// ToolCredentialsProxied hands the script credential-free configuration
	// pointing at a host-side proxy that injects the credentials on the way out.
	ToolCredentialsProxied ToolCredentials = "proxied"
)

func (c ToolCredentials) Valid() bool {
	return c == "" || c == ToolCredentialsNone || c == ToolCredentialsDirect || c == ToolCredentialsProxied
}

// SetupHasCredentials reports whether a setup carries secrets: any connection,
// or an env var resolved from a secret store. Static env vars and dotenv files
// are configuration and pass in every mode.
func SetupHasCredentials(setup shell.Setup) bool {
	return len(DeclaredConnections(setup.Connections)) > 0 || len(SecretEnvVarNames(setup)) > 0
}

// DeclaredConnections names every connection a setup declares. A declared
// connection counts even when its fields are empty: it still asks for
// credentials, and must not slip past a mode that forbids them.
func DeclaredConnections(connections connection.ExecConnections) []string {
	names := UnproxiableConnections(connections)
	if connections.Kubernetes != nil {
		names = append([]string{"kubernetes"}, names...)
	}
	return names
}

// SecretEnvVarNames names the env vars a setup resolves from a secret store.
func SecretEnvVarNames(setup shell.Setup) []string {
	var names []string
	for _, env := range setup.EnvVars {
		if env.ValueFrom != nil && !env.ValueFrom.IsEmpty() {
			names = append(names, env.Name)
		}
	}
	return names
}

// UnproxiableConnections names the connections no proxy can stand in for.
// Kubernetes is proxied by an auth-injecting API proxy behind a credential-free
// kubeconfig; cloud SDK credentials have no such proxy.
func UnproxiableConnections(connections connection.ExecConnections) []string {
	var names []string
	if connections.AWS != nil {
		names = append(names, "aws")
	}
	if connections.GCP != nil {
		names = append(names, "gcp")
	}
	if connections.Azure != nil {
		names = append(names, "azure")
	}
	if connections.FromConfigItem != nil {
		names = append(names, "fromConfigItem")
	}
	if connections.EKSPodIdentity {
		names = append(names, "eksPodIdentity")
	}
	if connections.ServiceAccount {
		names = append(names, "serviceAccount")
	}
	return names
}

func (t InlineTool) validateEnvironment() error {
	if !t.Placement.Valid() {
		return fmt.Errorf("invalid placement %q; want host or sandbox", t.Placement)
	}
	if !t.Credentials.Valid() {
		return fmt.Errorf("invalid credentials %q; want none, direct or proxied", t.Credentials)
	}
	for _, grant := range t.Grants {
		if err := grant.Validate(); err != nil {
			return err
		}
	}
	if len(t.Grants) > 0 && t.Credentials != "" && t.Credentials != ToolCredentialsProxied {
		return fmt.Errorf("grants are served by the credential proxy and need credentials: proxied, not %s", t.Credentials)
	}
	switch t.Credentials {
	case ToolCredentialsNone:
		if SetupHasCredentials(t.Setup) {
			return fmt.Errorf("credentials: none, but the tool declares connections or secret envVars")
		}
	case ToolCredentialsProxied:
		if names := UnproxiableConnections(t.Connections); len(names) > 0 {
			return fmt.Errorf("credentials: proxied, but the tool declares connections no proxy can serve (%s); use credentials: direct with placement: host",
				strings.Join(names, ", "))
		}
		if names := SecretEnvVarNames(t.Setup); len(names) > 0 {
			return fmt.Errorf("credentials: proxied, but the tool resolves secret envVars (%s) that would reach the script as real values; declare them as grants",
				strings.Join(names, ", "))
		}
	}
	return nil
}

// ResolveCredentials resolves the tool's credentials mode against the run's
// setup, which tools inherit from:
//
//   - none inherits no connection and no secret env var from the run;
//   - direct inherits all of them;
//   - proxied inherits only what a proxy can serve — the run's kubernetes
//     connection — and none of the run's real secrets.
//
// An unset mode is derived, never guessed: grants make it proxied; otherwise
// no credentials to hand over, the tool's own or the run's, make it none; on
// the host the tool then holds them directly; inside the sandbox it is refused,
// because secrets never enter a sandbox unless the prompt says how.
func (t InlineTool) ResolveCredentials(run shell.Setup) (ToolCredentials, error) {
	if t.Credentials != "" {
		return t.Credentials, nil
	}
	if len(t.Grants) > 0 {
		return ToolCredentialsProxied, nil
	}
	if !SetupHasCredentials(t.Setup) && !SetupHasCredentials(run) {
		return ToolCredentialsNone, nil
	}
	if t.Placement.Effective() == ToolPlacementHost {
		return ToolCredentialsDirect, nil
	}
	return "", fmt.Errorf("placement: sandbox with connections or secret envVars needs an explicit credentials: none, direct or proxied")
}
