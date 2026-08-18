package cli

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/captainconfig"
)

// putCredentialsConfig is the round trip the form performs: send a block, get
// back what was actually stored.
func putCredentialsConfig(t *testing.T, body string) *http.Response {
	t.Helper()
	return serveSandbox(t, loopbackRequest(
		http.MethodPut, "/api/captain/sandbox/credentials/config", body)).Result()
}

// Mirroring a login off this host is opt-in, and a destination naming nowhere
// would publish nothing while looking configured — so it is refused at the
// write rather than discovered as silence on the next tick.
func TestCredentialsConfigRefusesADestinationThatNamesNowhere(t *testing.T) {
	isolatedConfig(t)

	response := putCredentialsConfig(t, `{"refreshMargin":"1h","publish":[{"providers":["claude"]}]}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want a refusal", response.StatusCode)
	}

	// And nothing was written: a rejected block must not half-apply.
	saved, _, err := captainconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Credentials.Publish) != 0 || saved.Credentials.RefreshMargin != 0 {
		t.Fatalf("credentials = %+v, want the block untouched", saved.Credentials)
	}
}

// A duration crosses JSON as "1h" because that is what the yaml holds and what
// --refresh-margin takes; the nanosecond count time.Duration marshals to would
// be unreadable in the file this writes.
func TestCredentialsConfigRoundTripsADurationAsText(t *testing.T) {
	isolatedConfig(t)

	response := putCredentialsConfig(t,
		`{"refreshMargin":"90m","publish":[{"directory":"/tmp/creds","providers":["claude"]}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var echoed credentialsConfig
	if err := json.NewDecoder(response.Body).Decode(&echoed); err != nil {
		t.Fatal(err)
	}
	if echoed.RefreshMargin != "1h30m0s" {
		t.Fatalf("echoed margin = %q, want a duration string", echoed.RefreshMargin)
	}

	saved, _, err := captainconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Credentials.RefreshMargin != 90*time.Minute {
		t.Fatalf("stored margin = %s, want 90m", saved.Credentials.RefreshMargin)
	}
	if len(saved.Credentials.Publish) != 1 || saved.Credentials.Publish[0].Directory != "/tmp/creds" {
		t.Fatalf("stored publish = %+v", saved.Credentials.Publish)
	}
}

func TestCredentialsConfigRejectsAnUnparseableDuration(t *testing.T) {
	isolatedConfig(t)

	response := putCredentialsConfig(t, `{"refreshMargin":"soon","publish":[]}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want a refusal", response.StatusCode)
	}
}

// An empty publish list is the documented way to turn mirroring off, so it has
// to be storable rather than read as "no change".
func TestCredentialsConfigStoresAnEmptyPublishList(t *testing.T) {
	isolatedConfig(t)

	if code := putCredentialsConfig(t,
		`{"refreshMargin":"","publish":[{"directory":"/tmp/creds"}]}`).StatusCode; code != http.StatusOK {
		t.Fatalf("seed status = %d", code)
	}
	if code := putCredentialsConfig(t, `{"refreshMargin":"","publish":[]}`).StatusCode; code != http.StatusOK {
		t.Fatalf("clear status = %d", code)
	}

	saved, _, err := captainconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Credentials.Publish) != 0 {
		t.Fatalf("publish = %+v, want it cleared", saved.Credentials.Publish)
	}
}

// A Kubernetes destination is the shape the deploy form's "Agent login Secret"
// consumes, so the namespace/secret/context triple has to survive the trip.
func TestCredentialsConfigRoundTripsAKubernetesDestination(t *testing.T) {
	isolatedConfig(t)

	response := putCredentialsConfig(t,
		`{"refreshMargin":"","publish":[{"namespace":"captain","secret":"agent-creds","kubeContext":"k3s"}]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	saved, _, err := captainconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Credentials.Publish) != 1 {
		t.Fatalf("publish = %+v", saved.Credentials.Publish)
	}
	target := saved.Credentials.Publish[0].Kubernetes
	if target == nil {
		t.Fatal("kubernetes destination was stored as a directory")
	}
	if target.Namespace != "captain" || target.Secret != "agent-creds" || target.Context != "k3s" {
		t.Fatalf("kubernetes = %+v", *target)
	}
}

// The sync route writes a credential into a directory or a cluster, which is
// exactly why the CLI command is local-only. Reaching it from anywhere but
// loopback must be refused before it reads the keychain.
func TestCredentialsRoutesAreLoopbackOnly(t *testing.T) {
	isolatedConfig(t)

	for _, route := range []struct {
		method string
		target string
		body   string
	}{
		{http.MethodGet, "/api/captain/sandbox/credentials", ""},
		{http.MethodPut, "/api/captain/sandbox/credentials/config", `{"refreshMargin":"","publish":[]}`},
		{http.MethodPost, "/api/captain/sandbox/credentials/sync", `{}`},
	} {
		request := loopbackRequest(route.method, route.target, route.body)
		request.RemoteAddr = "203.0.113.7:41000"
		if code := serveSandbox(t, request).Code; code != http.StatusForbidden {
			t.Errorf("%s %s from off-host = %d, want 403", route.method, route.target, code)
		}
	}
}
