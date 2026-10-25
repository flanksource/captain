package adapter

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/types"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credproxy"
	"github.com/flanksource/captain/pkg/gitagent"
)

const (
	sidecarGrantSecret = "sidecar-test-grant-secret"
	sidecarKubeSecret  = "sidecar-test-kubeconfig-secret"
)

func secretGrant() credproxy.Grant {
	return credproxy.Grant{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"}, Paths: []string{"/"},
		Headers: []credproxy.HeaderGrant{{Name: "Authorization", Value: types.EnvVar{ValueStatic: sidecarGrantSecret}}}}
}

func TestSidecarToolsPublishProxiedToolsWithoutTheirCredentials(t *testing.T) {
	declared := api.InlineTools{
		"proxied": {Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsProxied, Exec: "env",
			Grants: []credproxy.Grant{secretGrant()}},
		"released": {Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsSidecarProxied, Exec: "env",
			Grants: []credproxy.Grant{secretGrant()}},
		"local": {Placement: api.ToolPlacementSupervisor, Exec: "env"},
	}
	proxied := declared["proxied"]
	proxied.Connections = connection.ExecConnections{Kubernetes: &connection.KubernetesConnection{
		KubeconfigConnection: connection.KubeconfigConnection{Kubeconfig: &types.EnvVar{ValueStatic: sidecarKubeSecret}},
	}}
	declared["proxied"] = proxied
	opts := api.RemoteExecOptions{
		CredentialReleases: map[string]api.CredentialRelease{"released": {Addr: "127.0.0.1:4001", Token: "release-token"}},
		ToolProxies: map[string]api.ToolProxyEndpoints{"proxied": {
			Egress: "127.0.0.1:4002", EgressSecret: "egress-secret", CAPEM: []byte("ca"),
			KubeURL: "http://127.0.0.1:4003", KubeToken: "kube-token",
		}},
	}
	services := gitagent.Services{}

	shipped, err := sidecarTools(declared, opts, services)
	if err != nil {
		t.Fatal(err)
	}

	wantServices := gitagent.Services{"creds/released": "127.0.0.1:4001", "egress/proxied": "127.0.0.1:4002", "kube/proxied": "127.0.0.1:4003"}
	if !reflect.DeepEqual(services, wantServices) {
		t.Fatalf("services = %#v, want %#v", services, wantServices)
	}
	wantProxies := map[string]gitagent.TaskToolProxies{"proxied": {
		EgressService: "egress/proxied", EgressSecret: "egress-secret", CAPEM: []byte("ca"),
		KubeService: "kube/proxied", KubeToken: "kube-token",
	}}
	if !reflect.DeepEqual(shipped.Proxies, wantProxies) {
		t.Fatalf("proxies = %#v, want %#v", shipped.Proxies, wantProxies)
	}
	if names := shipped.Tools.Names(); !reflect.DeepEqual(names, []string{"proxied", "released"}) {
		t.Fatalf("shipped tools = %v; a supervisor tool must stay behind", names)
	}
	payload, err := json.Marshal(shipped)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{sidecarGrantSecret, sidecarKubeSecret} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("the shipped payload carries %q: %s", secret, payload)
		}
	}
}

func TestSidecarToolsRefuseAProxiedToolTheRunServesNoProxiesFor(t *testing.T) {
	declared := api.InlineTools{"proxied": {Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsProxied,
		Exec: "env", Grants: []credproxy.Grant{secretGrant()}}}

	_, err := sidecarTools(declared, api.RemoteExecOptions{}, gitagent.Services{})

	if err == nil || !strings.Contains(err.Error(), "serves no proxies") {
		t.Fatalf("err = %v, want a refusal naming the missing proxies", err)
	}
}
