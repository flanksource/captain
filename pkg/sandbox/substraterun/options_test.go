// Tests a substrate backend's options: what ~/.captain.yaml spells out decodes into the
// substrate client's options and the environment's egress, and every mistake names its key.
package substraterun

import (
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/substrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pinned = "ghcr.io/flanksource/captain-runner@sha256:0000000000000000000000000000000000000000000000000000000000000000"

func backend(options map[string]any) api.SandboxConfig {
	base := map[string]any{"envAPI": "env.example:443", "control": "control.example:443", "image": pinned}
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
	assert.Equal(t, pinned, options.image)
	assert.Equal(t, []string{"gavel", "runner", "exec"}, options.command)
	anthropic, err := substrate.CredentialFor("anthropic", "api-key")
	require.NoError(t, err)
	anthropic.URI = "ate-secret://k8s.io/captain/anthropic/key"
	assert.Equal(t, substrate.Egress{
		Passthrough: []substrate.HostPort{{Host: "proxy.golang.org", Port: 443}},
		Credentials: []substrate.Credential{anthropic},
	}, options.egress)
}

func TestABackendRunsCaptainInItsEnvironmentByDefault(t *testing.T) {
	options, err := decodeOptions(backend(nil))
	require.NoError(t, err)
	assert.Equal(t, []string{"captain", "sandbox", "substrate", "run-task"}, options.command)
	assert.Equal(t, "ate-env", options.client.Atespace)
}

func TestABackendsMistakesNameTheirKey(t *testing.T) {
	for name, tc := range map[string]struct {
		options map[string]any
		want    string
	}{
		"unknown key":               {map[string]any{"envApi": "typo"}, `sandbox backend "beta-substrate": unknown option "envApi"`},
		"an unknown credential key": {map[string]any{"credentials": []any{map[string]any{"provider": "anthropic", "auth": "api-key", "URI": "ate-secret://x"}}}, `sandbox backend "beta-substrate": unknown option "credentials[0].URI"`},
		"no env API":                {map[string]any{"envAPI": ""}, `sandbox backend "beta-substrate": envAPI is required`},
		"no control plane":          {map[string]any{"control": ""}, `sandbox backend "beta-substrate": control is required`},
		"no image":                  {map[string]any{"image": ""}, `sandbox backend "beta-substrate": image is required`},
		"an image tag":              {map[string]any{"image": "ghcr.io/flanksource/captain-runner:v1"}, `sandbox backend "beta-substrate": image: substrate runs images pinned by digest: "ghcr.io/flanksource/captain-runner:v1" must be repo@sha256:<digest>`},
		"a lone cert":               {map[string]any{"clientCert": "/etc/captain/client.crt"}, `sandbox backend "beta-substrate": clientCert and clientKey go together`},
		"a credential URI":          {map[string]any{"credentials": []any{map[string]any{"provider": "anthropic", "auth": "api-key"}}}, `sandbox backend "beta-substrate": credentials[0] (anthropic api-key) needs the uri the egress gateway resolves its key from`},
		"a credential kind":         {map[string]any{"credentials": []any{map[string]any{"provider": "anthropic", "auth": "password", "uri": "ate-secret://x"}}}, `sandbox backend "beta-substrate": credentials[0]: `},
		"an empty command":          {map[string]any{"command": []any{}}, `sandbox backend "beta-substrate": command, when set, names the program the environment runs`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeOptions(backend(tc.options))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
