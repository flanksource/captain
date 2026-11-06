// The live Substrate cluster the substrate e2e tests run against: its API addresses, the token
// for its control plane, and the template environments are created from.
package substratetest

import (
	"os"
	"testing"

	"github.com/flanksource/captain/pkg/substrate"
)

// Cluster is a kind cluster with Substrate and env installed, as gavel's docs/sandbox.md sets up,
// as its environment variables override it, or a Substrate behind a client-certificate ingress.
type Cluster struct {
	// Options.Template is an image template with sh and coreutils.
	Options substrate.Options
}

// FromEnv skips t unless CAPTAIN_SUBSTRATE_TEST=1, and returns the cluster's settings.
func FromEnv(t testing.TB) Cluster {
	t.Helper()
	if os.Getenv("CAPTAIN_SUBSTRATE_TEST") != "1" {
		t.Skip("set CAPTAIN_SUBSTRATE_TEST=1 with a Substrate cluster to run the substrate e2e")
	}
	setting := func(name, fallback string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		if fallback == "" {
			t.Fatalf("set %s", name)
		}
		return fallback
	}
	return Cluster{
		Options: substrate.Options{
			EnvAPI:           setting("CAPTAIN_SUBSTRATE_ENV_API", "localhost:7777"),
			Control:          setting("CAPTAIN_SUBSTRATE_CONTROL", "localhost:8443"),
			ControlTokenFile: setting("CAPTAIN_SUBSTRATE_TOKEN_FILE", ""),
			Template:         setting("CAPTAIN_SUBSTRATE_TEMPLATE", "default-template"),
			Atespace:         os.Getenv("CAPTAIN_SUBSTRATE_ATESPACE"),
			CAFile:           os.Getenv("CAPTAIN_SUBSTRATE_CA_FILE"),
			ClientCertFile:   os.Getenv("CAPTAIN_SUBSTRATE_CLIENT_CERT"),
			ClientKeyFile:    os.Getenv("CAPTAIN_SUBSTRATE_CLIENT_KEY"),
		},
	}
}
