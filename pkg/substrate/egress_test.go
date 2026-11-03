// Specs for the egress policy an environment's actor gets: which hosts pass through, which take
// cleartext HTTP, which have a credential put into their requests, and which hosts are refused.
package substrate

import (
	"testing"

	"github.com/flanksource/captain/pkg/substrate/internal/ateapipb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEgressPolicyAllowsOnlyWhatItNames(t *testing.T) {
	policy, err := egressPolicy("ate-env", Egress{
		Passthrough: []HostPort{{Host: "gavel.example.com", Port: 443}, {Host: "proxy.golang.org", Port: 443}, {Host: "git.example.com", Port: 8443}},
		HTTP:        []HostPort{{Host: "gavel.gavel.svc.cluster.local", Port: 8080}},
		Credentials: []Credential{{Host: "api.anthropic.com", Header: "Authorization", Prefix: "Bearer ", URI: "ate-secret://k8s.io/captain/anthropic/key"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ate-env", policy.GetMetadata().GetAtespace())
	assert.Equal(t, "default", policy.GetMetadata().GetName())
	assert.Equal(t, []*ateapipb.EgressRule{
		{TlsPassthrough: &ateapipb.TlsPassthroughRule{Hostnames: []string{"gavel.example.com", "proxy.golang.org"}, Ports: &ateapipb.Ports{Numbers: []int32{443}}}},
		{TlsPassthrough: &ateapipb.TlsPassthroughRule{Hostnames: []string{"git.example.com"}, Ports: &ateapipb.Ports{Numbers: []int32{8443}}}},
		{Http: &ateapipb.HttpRule{Hostnames: []string{"gavel.gavel.svc.cluster.local"}, Ports: &ateapipb.Ports{Numbers: []int32{8080}}}},
		{Https: &ateapipb.HttpsRule{
			Hostnames: []string{"api.anthropic.com"},
			Effects: &ateapipb.HttpRuleEffects{ReplaceHeaders: []*ateapipb.CredentialHeader{{
				Header: "Authorization", Prefix: "Bearer ", CredentialUri: "ate-secret://k8s.io/captain/anthropic/key",
			}}},
		}},
	}, policy.GetRules())
}

func TestEgressPolicyRefusesWhatSubstrateCannotEnforce(t *testing.T) {
	for name, tc := range map[string]struct {
		egress Egress
		err    string
	}{
		"an IP address": {
			Egress{HTTP: []HostPort{{Host: "10.0.0.5", Port: 8080}}},
			`substrate egress names hosts by DNS name, not IP address: "10.0.0.5"`,
		},
		"a port out of range": {
			Egress{Passthrough: []HostPort{{Host: "gavel.example.com", Port: 70000}}},
			`substrate egress to gavel.example.com: port 70000 is outside 1-65535`,
		},
		"no host": {
			Egress{Passthrough: []HostPort{{Port: 443}}},
			`substrate egress names a port with no host`,
		},
		"a credential without its URI": {
			Egress{Credentials: []Credential{{Host: "api.anthropic.com", Header: "x-api-key"}}},
			`the credential for api.anthropic.com needs its host, header and URI`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := egressPolicy("ate-env", tc.egress)
			require.EqualError(t, err, tc.err)
		})
	}
}
