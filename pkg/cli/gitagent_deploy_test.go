package cli

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/database"
)

// deployOptions is a valid docker deployment, for mutation by each test.
func deployOptions(name string) GitAgentDeployOptions {
	return GitAgentDeployOptions{
		Name: name, Backend: "git-agent", Target: "docker",
		Image: "ghcr.io/flanksource/captain:latest", Home: "/home/claude",
		ListenPort: 7422,
		CPURequest: "500m", CPULimit: "2",
		MemoryRequest: "1Gi", MemoryLimit: "4Gi",
		Storage: "20Gi", TmpSize: "1Gi", PidsLimit: 1024,
		RunAsUser: 501, RunAsGroup: 20, ReadOnlyRoot: true, Network: "bridge",
		Timeout: "5m", DryRun: true,
		// Skips the off-loopback probe, which a sandboxed test host cannot pass.
		SupervisorAddress: "ssh://host.docker.internal:7422",
	}
}

// liveMailbox starts a listener presenting this host's git-agent host key and
// records it as the backend's mailbox, which is what detection requires.
func liveMailbox(t *testing.T, backend string) {
	t.Helper()
	listener := serveHostKey(t, hostSigner(t))
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	recordMailboxListening(t, backend, ":"+port)
}

func TestDeployRefusesBeforeMintingAToken(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*GitAgentDeployOptions)
		wantErr string
	}{
		{"no target", func(o *GitAgentDeployOptions) { o.Target = "" }, "--target is required"},
		{"unknown target", func(o *GitAgentDeployOptions) { o.Target = "podman" }, "docker, kubernetes"},
		{"bad quantity", func(o *GitAgentDeployOptions) { o.MemoryLimit = "4GB" }, "--memory-limit"},
		{"bad timeout", func(o *GitAgentDeployOptions) { o.Timeout = "soon" }, "not a duration"},
		{"root user", func(o *GitAgentDeployOptions) { o.RunAsUser = 0 }, "--run-as-user 0"},
		{"host network", func(o *GitAgentDeployOptions) { o.Network = "host" }, "host network namespace"},
		{"no network", func(o *GitAgentDeployOptions) { o.Network = "none" }, "dispatch, relay"},
		{"invalid agent name", func(o *GitAgentDeployOptions) { o.Name = "Worker 01" }, "agent name"},
		// An ignored --domain would look configured and leave the operator
		// waiting on a hostname nothing ever created.
		{"a route on docker", func(o *GitAgentDeployOptions) { o.Domain = "agents.example.com" },
			"--domain needs --target kubernetes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := isolatedConfig(t)
			liveMailbox(t, "git-agent")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			opts := deployOptions("worker-01")
			tt.mutate(&opts)
			if _, err := RunGitAgentDeploy(t.Context(), opts); err == nil ||
				!strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("a refused deploy mutated the config; a token may have been minted")
			}
		})
	}
}

// A preset that grants the container runtime socket is a full host escape
// (R5.3/A6.2), and it must be caught from the backend config, not just flags.
func TestDeployRefusesABackendGrantingTheRuntimeSocket(t *testing.T) {
	isolatedConfig(t)
	liveMailbox(t, "git-agent")

	err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, "git-agent")
		if err != nil {
			return err
		}
		backend.Options["presets"] = []any{"golang", "claude"}
		cfg.Sandbox.Backends["git-agent"] = backend
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := RunGitAgentDeploy(t.Context(), deployOptions("worker-01")); err == nil ||
		!strings.Contains(err.Error(), "container runtime socket") {
		t.Fatalf("err = %v, want a refusal naming the runtime socket", err)
	}
}

// RecordAgent overwrites an agent entry wholesale, so a silent re-enroll would
// repoint the supervisor at a new key and leave the old sidecar running with
// one that is no longer authorized.
func TestDeployRefusesAnAlreadyEnrolledNameWithoutReplace(t *testing.T) {
	isolatedConfig(t)
	liveMailbox(t, "git-agent")

	err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, "git-agent")
		if err != nil {
			return err
		}
		backend.Options["agents"] = map[string]any{
			"worker-01": map[string]any{"fingerprint": "SHA256:existing"},
		}
		cfg.Sandbox.Backends["git-agent"] = backend
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = RunGitAgentDeploy(t.Context(), deployOptions("worker-01"))
	if err == nil || !strings.Contains(err.Error(), "already enrolled") {
		t.Fatalf("err = %v, want a refusal to rebind the name", err)
	}
	if !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("the refusal must name the way forward: %v", err)
	}
}

func TestDeployDryRunRendersTheHardenedArgvWithoutMutating(t *testing.T) {
	path := isolatedConfig(t)
	liveMailbox(t, "git-agent")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res, err := RunGitAgentDeploy(t.Context(), deployOptions("worker-01"))
	if err != nil {
		t.Fatal(err)
	}
	result, ok := res.(GitAgentDeployResult)
	if !ok {
		t.Fatalf("result = %T", res)
	}
	if !result.DryRun || result.Enrolled {
		t.Fatalf("dry run reported enrolled: %+v", result)
	}
	// The mailbox's real host key, proven by the probe rather than assumed.
	if result.HostFingerprint == "" {
		t.Fatal("no mailbox host key was proven")
	}
	if result.SupervisorFrom != "flag" || result.AdvertiseFrom != "docker-published-port" {
		t.Fatalf("addresses not resolved as expected: %+v", result)
	}
	// An operator who does not know this finds out at the first dispatch.
	if !strings.Contains(result.Credentials, "none declared") {
		t.Fatalf("credentials = %q, want it to flag that none were declared", result.Credentials)
	}
	if result.EgressRestricted {
		t.Fatal("egress is not actually restricted today; reporting otherwise is a lie")
	}
	// deployOptions passes --supervisor-address, which skips the off-loopback
	// proof entirely. Reporting addresses that were never probed would be the
	// same lie as EgressRestricted above.
	if len(result.OffHostAddresses) != 0 {
		t.Fatalf("off-loopback proof = %v, but --supervisor-address skipped the probe", result.OffHostAddresses)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("--dry-run mutated the config")
	}
}

// The externally-routed topology, rendered without touching a cluster: the
// operator has to see the Ingress, the certificate source, and — most
// importantly — the DNS record captain will NOT create for them.
func TestKubernetesDryRunRendersTheIngress(t *testing.T) {
	isolatedConfig(t)
	liveMailbox(t, "git-agent")

	opts := deployOptions("worker-01")
	opts.Target = "kubernetes"
	opts.Namespace = "agents"
	opts.SupervisorAddress = "https://mailbox.example.com:9020"
	opts.Domain = "agents.example.com"
	opts.IngressClass = "nginx"
	opts.IngressIssuer = "letsencrypt-prod"

	res, err := RunGitAgentDeploy(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := res.(GitAgentDeployResult)
	if !ok {
		t.Fatalf("result = %T", res)
	}

	want := "https://worker-01.agents.example.com/git/" + SidecarRepoName
	if result.Advertise != want || result.AdvertiseFrom != "cluster-ingress" {
		t.Fatalf("advertise = %q from %q, want %q", result.Advertise, result.AdvertiseFrom, want)
	}
	if result.Route != "worker-01.agents.example.com" || result.RouteClass != "nginx" {
		t.Fatalf("route = %q class %q", result.Route, result.RouteClass)
	}

	mutations := strings.Join(result.Mutations, "\n")
	for _, name := range []string{
		"Ingress/captain-git-agent-worker-01",
		"Secret/captain-git-agent-worker-01-tls",
		"letsencrypt-prod",
		// The most consequential thing about this feature is a change it does
		// not make, so the dry run has to say so.
		"NOT create the DNS record",
	} {
		if !strings.Contains(mutations, name) {
			t.Errorf("mutations do not mention %q:\n%s", name, mutations)
		}
	}
}

// Without a route and without a supervisor inside the cluster there is no
// address to advertise that a dispatch could reach.
func TestKubernetesWithoutARouteRefusesToAdvertise(t *testing.T) {
	isolatedConfig(t)
	liveMailbox(t, "git-agent")

	opts := deployOptions("worker-01")
	opts.Target = "kubernetes"
	opts.Namespace = "agents"
	opts.SupervisorAddress = "https://mailbox.example.com:9020"

	_, err := RunGitAgentDeploy(t.Context(), opts)
	if err == nil || !strings.Contains(err.Error(), "--domain") {
		t.Fatalf("err = %v, want a demand for a reachable route", err)
	}
}

// A durable token that nothing claims does not lapse on its own, so a deploy
// that dies after the mint has to retire the credential it created — otherwise
// it leaves a live way in for a workload that never started.
func TestRevokeUnclaimedTokenRetiresOnlyItsOwnAgent(t *testing.T) {
	isolatedConfig(t)
	db := gitAgentTokenDB(t)
	const backend = "git-agent"

	minted := map[string]GitAgentAddResult{}
	for _, name := range []string{"worker-01", "worker-02"} {
		res, err := RunGitAgentAdd(t.Context(), GitAgentAddOptions{Name: name, Backend: backend})
		if err != nil {
			t.Fatal(err)
		}
		minted[name] = res.(GitAgentAddResult)
	}

	if err := revokeUnclaimedToken(t.Context(), minted["worker-01"].TokenID, "worker-01"); err != nil {
		t.Fatal(err)
	}

	live, err := db.ListAPITokens(t.Context(), database.ListAPITokensFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Agent != "worker-02" {
		t.Fatalf("live tokens = %+v; only the other agent's should survive", live)
	}

	revoked, err := db.GetAPIToken(t.Context(), minted["worker-01"].TokenID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt == nil || !strings.Contains(revoked.RevocationReason, "worker-01") {
		t.Fatalf("revocation should name the agent and why: %+v", revoked)
	}

	// Nothing to withdraw is not a failure: a deploy can fail before the mint.
	if err := revokeUnclaimedToken(t.Context(), "", "worker-03"); err != nil {
		t.Fatal(err)
	}
}
