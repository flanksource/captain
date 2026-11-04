// Tests a substrate backend's options: what ~/.captain.yaml spells out decodes into the
// substrate client's options and the environment's egress, and every mistake names its key.
package substraterun

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/substrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func backend(options map[string]any) api.SandboxConfig {
	base := map[string]any{"envAPI": "env.example:443", "control": "control.example:443", "template": "captain-runner"}
	for key, value := range options {
		base[key] = value
	}
	return api.SandboxConfig{Kind: api.SandboxSubstrate, Name: "beta-substrate", Options: base}
}

func TestABackendsOptionsReachTheSubstrateClient(t *testing.T) {
	options, err := decodeOptions(backend(map[string]any{
		"tokenFile": "/etc/captain/token", "caFile": "/etc/captain/ca.pem",
		"clientCert": "/etc/captain/client.crt", "clientKey": "/etc/captain/client.key",
		"atespace": "ate-captain", "template": "captain-runner", "trustBundle": "/run/ate/trust-bundle.pem",
		"egress":      []any{"proxy.golang.org"},
		"credentials": []any{map[string]any{"provider": "anthropic", "auth": "api-key", "uri": "ate-secret://k8s.io/captain/anthropic/key"}},
		"command":     []any{"gavel", "runner", "exec"},
	}))
	require.NoError(t, err)
	assert.Equal(t, substrate.Options{
		EnvAPI: "env.example:443", Control: "control.example:443", ControlTokenFile: "/etc/captain/token",
		CAFile: "/etc/captain/ca.pem", ClientCertFile: "/etc/captain/client.crt", ClientKeyFile: "/etc/captain/client.key",
		Atespace: "ate-captain", Template: "captain-runner", TrustBundle: "/run/ate/trust-bundle.pem",
	}, options.client)
	assert.Equal(t, []string{"gavel", "runner", "exec"}, options.command)
	anthropic, err := substrate.CredentialFor("anthropic", "api-key")
	require.NoError(t, err)
	anthropic.URI = "ate-secret://k8s.io/captain/anthropic/key"
	assert.Equal(t, substrate.Egress{
		Passthrough: []substrate.HostPort{{Host: "proxy.golang.org", Port: 443}},
		Credentials: []substrate.Credential{anthropic},
	}, options.egress)
}

func TestACredentialCanBeGivenInFull(t *testing.T) {
	options, err := decodeOptions(backend(map[string]any{"credentials": []any{map[string]any{
		"host": "api.anthropic.com", "header": "Authorization", "prefix": "Bearer ", "env": "CLAUDE_CODE_OAUTH_TOKEN", "uri": "ate-secret://gavel/run-1/anthropic",
	}}}))
	require.NoError(t, err)
	assert.Equal(t, []substrate.Credential{{
		Host: "api.anthropic.com", Header: "Authorization", Prefix: "Bearer ", Env: "CLAUDE_CODE_OAUTH_TOKEN", URI: "ate-secret://gavel/run-1/anthropic",
	}}, options.egress.Credentials)
}

func TestEgressReachesHostsOverTLSOrInCleartextOnTheirPorts(t *testing.T) {
	options, err := decodeOptions(backend(map[string]any{"egress": []any{"proxy.golang.org", "git.example:8443", "http://gavel.gavel.svc:8080"}}))
	require.NoError(t, err)
	assert.Equal(t, substrate.Egress{
		Passthrough: []substrate.HostPort{{Host: "proxy.golang.org", Port: 443}, {Host: "git.example", Port: 8443}},
		HTTP:        []substrate.HostPort{{Host: "gavel.gavel.svc", Port: 8080}},
	}, options.egress)
}

func TestARunsSelectionNamesItsEnvironmentAndCommit(t *testing.T) {
	options, err := decodeOptions(backend(map[string]any{"environment": "gavel-run-1", "ref": "main", "env": map[string]any{"GAVEL_RUN_ID": "run-1"}}))
	require.NoError(t, err)
	assert.Equal(t, "gavel-run-1", options.environment)
	assert.Equal(t, "main", options.ref)
	assert.Equal(t, map[string]string{"GAVEL_RUN_ID": "run-1"}, options.env)
}

func TestABackendRunsCaptainInItsEnvironmentByDefault(t *testing.T) {
	options, err := decodeOptions(backend(nil))
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, options.maxLifetime)
	assert.Equal(t, []string{"captain", "sandbox", "substrate", "run-task"}, options.command)
	assert.Equal(t, "ate-env", options.client.Atespace)
}

func TestABackendsMistakesNameTheirKey(t *testing.T) {
	for name, tc := range map[string]struct {
		options map[string]any
		want    string
	}{
		"unknown key":                  {map[string]any{"envApi": "typo"}, `sandbox backend "beta-substrate": unknown option "envApi"`},
		"an unknown credential key":    {map[string]any{"credentials": []any{map[string]any{"provider": "anthropic", "auth": "api-key", "URI": "ate-secret://x"}}}, `sandbox backend "beta-substrate": unknown option "credentials[0].URI"`},
		"no env API":                   {map[string]any{"envAPI": ""}, `sandbox backend "beta-substrate": envAPI is required`},
		"no control plane":             {map[string]any{"control": ""}, `sandbox backend "beta-substrate": control is required`},
		"no template":                  {map[string]any{"template": ""}, `sandbox backend "beta-substrate": template is required: it names the image template, registered with ate-env manifest template --rootfs-image, that the run's environment is created from`},
		"an image":                     {map[string]any{"image": "ghcr.io/flanksource/captain-runner@sha256:0"}, `sandbox backend "beta-substrate": unknown option "image"`},
		"a lone cert":                  {map[string]any{"clientCert": "/etc/captain/client.crt"}, `sandbox backend "beta-substrate": clientCert and clientKey go together`},
		"a credential URI":             {map[string]any{"credentials": []any{map[string]any{"provider": "anthropic", "auth": "api-key"}}}, `sandbox backend "beta-substrate": credentials[0] (anthropic api-key) needs the uri the egress gateway resolves its key from`},
		"a credential kind":            {map[string]any{"credentials": []any{map[string]any{"provider": "anthropic", "auth": "password", "uri": "ate-secret://x"}}}, `sandbox backend "beta-substrate": credentials[0]: `},
		"an empty command":             {map[string]any{"command": []any{}}, `sandbox backend "beta-substrate": command, when set, names the program the environment runs`},
		"a lifetime":                   {map[string]any{"maxLifetime": "soon"}, `sandbox backend "beta-substrate": maxLifetime "soon": time: invalid duration "soon"`},
		"a negative lifetime":          {map[string]any{"maxLifetime": "-1h"}, `sandbox backend "beta-substrate": maxLifetime must be positive, got "-1h"`},
		"an egress port":               {map[string]any{"egress": []any{"git.example:http"}}, `sandbox backend "beta-substrate": egress "git.example:http": the port must be a number`},
		"an egress scheme":             {map[string]any{"egress": []any{"ftp://files.example"}}, `sandbox backend "beta-substrate": egress "ftp://files.example": a host, host:port, or http://host[:port]`},
		"a credential half given":      {map[string]any{"credentials": []any{map[string]any{"host": "api.anthropic.com", "uri": "ate-secret://x"}}}, `sandbox backend "beta-substrate": credentials[0] names the host, header and env the gateway injects into, or a provider and auth, not some of them`},
		"a credential given both ways": {map[string]any{"credentials": []any{map[string]any{"provider": "anthropic", "auth": "api-key", "host": "api.anthropic.com", "header": "x-api-key", "env": "ANTHROPIC_API_KEY", "uri": "ate-secret://x"}}}, `sandbox backend "beta-substrate": credentials[0] names the host, header and env the gateway injects into, or a provider and auth, not some of them`},
		"an environment ID":            {map[string]any{"environment": "Gavel Run"}, `sandbox backend "beta-substrate": environment "Gavel Run" must be lowercase letters, digits and '-', starting with a letter`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeOptions(backend(tc.options))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
