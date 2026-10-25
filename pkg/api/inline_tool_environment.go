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
	// ToolCredentialsSidecar resolves the tool's own connections and secret env
	// vars on the sidecar, from its environment, files and cloud identity. The
	// supervisor's secrets are never involved.
	ToolCredentialsSidecar ToolCredentials = "sidecar"
	// ToolCredentialsSidecarProxied runs the credential proxy on the sidecar: it
	// fetches the tool's grant values from the supervisor once and keeps them in
	// memory, and the script sees only placeholders.
	ToolCredentialsSidecarProxied ToolCredentials = "sidecar-proxied"
)

func (c ToolCredentials) Valid() bool {
	switch c {
	case "", ToolCredentialsNone, ToolCredentialsDirect, ToolCredentialsProxied, ToolCredentialsSidecar, ToolCredentialsSidecarProxied:
		return true
	}
	return false
}

// proxiesGrants reports whether a mode serves grants through a credential
// proxy, wherever that proxy runs.
func (c ToolCredentials) proxiesGrants() bool {
	return c == ToolCredentialsProxied || c == ToolCredentialsSidecarProxied
}

// ValidateToolCredentials refuses a credentials mode the placement cannot
// honour: sidecar modes exist only on a sidecar, and a sidecar never receives
// the supervisor's real secrets.
func ValidateToolCredentials(placement ToolPlacement, credentials ToolCredentials) error {
	sidecarMode := credentials == ToolCredentialsSidecar || credentials == ToolCredentialsSidecarProxied
	switch {
	case sidecarMode && placement != ToolPlacementSidecar:
		return fmt.Errorf("credentials: %s needs placement: sidecar, not %s", credentials, placementName(placement))
	case placement == ToolPlacementSidecar && credentials == ToolCredentialsDirect:
		return fmt.Errorf("credentials: direct would send the supervisor's secrets to the sidecar; use sidecar (the sidecar's own credentials), sidecar-proxied or proxied")
	}
	return nil
}

func placementName(placement ToolPlacement) string {
	if placement == "" {
		return "the sandbox's default"
	}
	return string(placement)
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
		return fmt.Errorf("invalid placement %q; want host, sandbox, supervisor or sidecar", t.Placement)
	}
	if !t.Credentials.Valid() {
		return fmt.Errorf("invalid credentials %q; want none, direct, proxied, sidecar or sidecar-proxied", t.Credentials)
	}
	if t.Placement != "" && t.Credentials != "" {
		if err := ValidateToolCredentials(t.Placement, t.Credentials); err != nil {
			return err
		}
	}
	for _, grant := range t.Grants {
		if err := grant.Validate(); err != nil {
			return err
		}
	}
	if len(t.Grants) > 0 && t.Credentials != "" && !t.Credentials.proxiesGrants() {
		return fmt.Errorf("grants are served by a credential proxy and need credentials: proxied or sidecar-proxied, not %s", t.Credentials)
	}
	switch t.Credentials {
	case ToolCredentialsNone:
		if SetupHasCredentials(t.Setup) {
			return fmt.Errorf("credentials: none, but the tool declares connections or secret envVars")
		}
	case ToolCredentialsSidecarProxied:
		if len(t.Grants) == 0 {
			return fmt.Errorf("credentials: sidecar-proxied serves grants and the tool declares none")
		}
		if names := DeclaredConnections(t.Connections); len(names) > 0 {
			return fmt.Errorf("credentials: sidecar-proxied serves only HTTP(S) grants, but the tool declares connections (%s); use credentials: proxied",
				strings.Join(names, ", "))
		}
		if names := SecretEnvVarNames(t.Setup); len(names) > 0 {
			return fmt.Errorf("credentials: sidecar-proxied, but the tool resolves secret envVars (%s); declare them as grants",
				strings.Join(names, ", "))
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
// The sidecar modes inherit nothing from the run: its setup is the
// supervisor's, and the sidecar resolves only what the tool itself declares.
//
// placement is the resolved placement (Sandbox.ResolveToolPlacement). An unset
// mode is derived, never guessed: grants make it proxied; otherwise no
// credentials to hand over, the tool's own or the run's, make it none; next to
// Captain the tool then holds them directly; a confined tool is refused,
// because secrets never enter a sandbox or reach a sidecar unless the prompt
// says how.
func (t InlineTool) ResolveCredentials(placement ToolPlacement, run shell.Setup) (ToolCredentials, error) {
	credentials, err := t.deriveCredentials(placement, run)
	if err != nil {
		return "", err
	}
	if err := ValidateToolCredentials(placement, credentials); err != nil {
		return "", err
	}
	return credentials, nil
}

func (t InlineTool) deriveCredentials(placement ToolPlacement, run shell.Setup) (ToolCredentials, error) {
	switch {
	case t.Credentials != "":
		return t.Credentials, nil
	case len(t.Grants) > 0:
		return ToolCredentialsProxied, nil
	case !SetupHasCredentials(t.Setup) && !SetupHasCredentials(run):
		return ToolCredentialsNone, nil
	case !placement.Confined():
		return ToolCredentialsDirect, nil
	case placement == ToolPlacementSidecar:
		return "", fmt.Errorf("placement: sidecar with connections or secret envVars needs an explicit credentials: none, proxied, sidecar or sidecar-proxied")
	}
	return "", fmt.Errorf("placement: %s with connections or secret envVars needs an explicit credentials: none, direct or proxied", placement)
}
