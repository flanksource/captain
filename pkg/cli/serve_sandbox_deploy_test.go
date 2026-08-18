package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

// gitAgentEnrollmentFor is a complete, dispatchable enrollment — RecordAgent
// refuses one missing an endpoint or a host key.
func gitAgentEnrollmentFor(name string) gitagent.AgentEnrollment {
	return gitagent.AgentEnrollment{
		Name: name, Fingerprint: "SHA256:" + name,
		URL: "ssh://" + name + ":7422/repo.git", HostFingerprint: "SHA256:host-" + name,
	}
}

// The preflight is what the UI blocks on, so a host that cannot deploy has to
// say so as data rather than as a failed request: "no mailbox has served here"
// is the expected answer on a fresh machine, and a 500 would read as a bug.
//
// Targeted at kubernetes rather than docker so the assertion is about the
// mailbox: the docker branch probes a daemon first, and on a machine without one
// the reason under test would be replaced by "docker did not answer".
func TestDeployPreflightReportsRefusalAsData(t *testing.T) {
	isolatedConfig(t)

	preflight := decodePreflight(t, "kubernetes")
	if preflight.Ready {
		t.Fatal("a host with no mailbox recorded must not report itself ready to deploy")
	}
	if preflight.Reason == "" {
		t.Fatal("a refusal with no reason gives an operator nothing to act on")
	}
	// The refusal names both ways to fix it, which is the whole reason it is
	// surfaced rather than left for the deploy to discover.
	for _, want := range []string{"captain serve", "serve --role mailbox"} {
		if !strings.Contains(preflight.Reason, want) {
			t.Fatalf("reason %q should name %q", preflight.Reason, want)
		}
	}
}

// Kubernetes cannot prove a route back to this host, so the address is the
// operator's to supply. The flag tells the form to demand one rather than
// letting a deploy fail after the token is minted.
func TestDeployPreflightMarksKubernetesSupervisorRequired(t *testing.T) {
	isolatedConfig(t)

	w := serveSandbox(t, loopbackRequest(http.MethodGet,
		"/api/captain/sandbox/git-agent/deploy/preflight?target=kubernetes", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	var preflight gitAgentDeployPreflight
	if err := json.Unmarshal(w.Body.Bytes(), &preflight); err != nil {
		t.Fatalf("decode preflight: %v", err)
	}
	if !preflight.SupervisorRequired {
		t.Fatal("kubernetes must require an explicit supervisor address")
	}
	if preflight.Target != string(deploy.TargetKubernetes) {
		t.Fatalf("target = %q", preflight.Target)
	}
}

// A probe that ran out of time and a probe that answered "no" call for
// different next steps, and only one of them means something is wrong. The
// preflight runs on modal open, and `docker info` against a stopped daemon can
// hang for tens of seconds.
func TestPreflightDistinguishesATimeoutFromARefusal(t *testing.T) {
	refused := errors.New("docker daemon is not reachable")

	if got := preflightReason(t.Context(), refused, "docker did not answer"); got != refused.Error() {
		t.Fatalf("a live context must report the real error, got %q", got)
	}

	expired, cancel := context.WithCancel(t.Context())
	cancel()
	got := preflightReason(expired, refused, "docker did not answer")
	if !strings.Contains(got, "docker did not answer") || !strings.Contains(got, "re-check") {
		t.Fatalf("a timed-out probe must say so and what to do, got %q", got)
	}
	if strings.Contains(got, "not reachable") {
		t.Fatalf("a timeout must not be reported as a refusal, got %q", got)
	}
}

// The mailbox `captain serve` hosts has to be visible to the UI as the one that
// answered, or an operator on an https supervisor is told to start a second
// process they do not need. The kubernetes target is used because it reaches the
// mailbox probe without a docker daemon.
func TestDeployPreflightReportsTheHTTPSMailboxItProbed(t *testing.T) {
	isolatedConfig(t)
	listen, pin := serveTLSPresenting(t)
	recordMailbox(t, "git-agent", mailboxRecord{
		Transport: transportHTTPS, Listen: listen, Identity: pin, Encrypted: true,
	})

	preflight := decodePreflight(t, "kubernetes")
	if preflight.Transport != string(transportHTTPS) {
		t.Fatalf("transport = %q, want https", preflight.Transport)
	}
	if preflight.MailboxListen != listen || preflight.HostFingerprint != pin {
		t.Fatalf("preflight = %+v, want the probed address and pin", preflight)
	}
}

// The kubernetes form makes the supervisor address mandatory, so the preflight
// offers this host's own addresses to fill it with. What the count is depends on
// the machine — a sandboxed runner may hold none — so the contract asserted here
// is that every offer is directly usable: the probed mailbox's scheme and port,
// and never loopback, which is the one address a pod certainly cannot reach.
func TestDeployPreflightOffersThisHostsAddressesForTheSupervisor(t *testing.T) {
	isolatedConfig(t)
	listen, pin := serveTLSPresenting(t)
	recordMailbox(t, "git-agent", mailboxRecord{
		Transport: transportHTTPS, Listen: listen, Identity: pin, Encrypted: true,
	})
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		t.Fatal(err)
	}

	candidates := decodePreflight(t, "kubernetes").SupervisorCandidates
	// Guard rather than assertion, so the checks below are never vacuous on a
	// machine that holds addresses and never fail on a runner that holds none.
	if held, err := hostInterfaceIPs(); err != nil || len(usableHostIPs(held)) == 0 {
		t.Skip("this host holds no address outside loopback, so there is nothing to offer")
	}
	if len(candidates) == 0 {
		t.Fatal("this host holds a usable address but the preflight offered none")
	}

	for _, candidate := range candidates {
		parsed, err := url.Parse(candidate)
		if err != nil {
			t.Errorf("candidate %q is not a URL: %v", candidate, err)
			continue
		}
		if parsed.Scheme != string(transportHTTPS) || parsed.Port() != port {
			t.Errorf("candidate %q does not address the probed mailbox on %s", candidate, listen)
		}
		if ip := net.ParseIP(parsed.Hostname()); ip == nil || ip.IsLoopback() {
			t.Errorf("candidate %q is not a non-loopback address of this host", candidate)
		}
	}
}

// The refusal a default `captain serve` earns. It is the whole point of
// recording the unusable state: the operator has a supervisor running and needs
// two flags, not a second process.
func TestDeployPreflightRefusesAPlainHTTPServe(t *testing.T) {
	isolatedConfig(t)
	recordMailbox(t, "git-agent", mailboxRecord{Transport: transportHTTPS, Listen: "localhost:9020"})

	preflight := decodePreflight(t, "kubernetes")
	if preflight.Ready {
		t.Fatal("a mailbox serving plain HTTP must not report itself deployable")
	}
	for _, want := range []string{"plain HTTP", "--tls"} {
		if !strings.Contains(preflight.Reason, want) {
			t.Fatalf("reason = %q, want it to name %q", preflight.Reason, want)
		}
	}
}

func decodePreflight(t *testing.T, target string) gitAgentDeployPreflight {
	t.Helper()
	w := serveSandbox(t, loopbackRequest(http.MethodGet,
		"/api/captain/sandbox/git-agent/deploy/preflight?target="+target, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	var preflight gitAgentDeployPreflight
	if err := json.Unmarshal(w.Body.Bytes(), &preflight); err != nil {
		t.Fatalf("decode preflight: %v", err)
	}
	return preflight
}

func TestDeployPreflightRejectsAnUnknownTarget(t *testing.T) {
	isolatedConfig(t)

	for _, target := range []string{"", "podman", "Docker%20Swarm"} {
		w := serveSandbox(t, loopbackRequest(http.MethodGet,
			"/api/captain/sandbox/git-agent/deploy/preflight?target="+target, ""))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("target %q: status = %d, want 400", target, w.Code)
		}
	}
}

// The deploy and undeploy routes create and destroy real infrastructure, so
// they stay on the same loopback-only footing as the rest of the configuration
// surface rather than riding the roster's read-only exemption.
func TestDeployRoutesAreLoopbackOnly(t *testing.T) {
	isolatedConfig(t)

	deployRequest := loopbackRequest(http.MethodPost,
		"/api/captain/sandbox/git-agent/deployments", `{"name":"worker-01","target":"docker"}`)
	deployRequest.RemoteAddr = "10.1.2.3:54321"
	if w := serveSandbox(t, deployRequest); w.Code != http.StatusForbidden {
		t.Fatalf("remote deploy status = %d, want 403", w.Code)
	}

	undeployRequest := loopbackRequest(http.MethodDelete,
		"/api/captain/sandbox/git-agent/deployments/worker-01", "")
	undeployRequest.RemoteAddr = "10.1.2.3:54321"
	if w := serveSandbox(t, undeployRequest); w.Code != http.StatusForbidden {
		t.Fatalf("remote undeploy status = %d, want 403", w.Code)
	}
}

func TestDeployRouteRejectsAMissingName(t *testing.T) {
	isolatedConfig(t)

	w := serveSandbox(t, loopbackRequest(http.MethodPost,
		"/api/captain/sandbox/git-agent/deployments", `{"target":"docker"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
}

// A blank field in the form means "leave it alone", so the request omits it and
// the CLI's own default applies. Restating the defaults in TypeScript would let
// the UI keep deploying a stale sizing after the flag moved on.
func TestDeployRequestFallsBackToCLIDefaults(t *testing.T) {
	opts := gitAgentDeployRequest{
		Name: "worker-01", GitAgentDeploymentConfig: GitAgentDeploymentConfig{Target: "docker"},
	}.options("git-agent")

	if opts.Image != "ghcr.io/flanksource/captain:latest" {
		t.Fatalf("image = %q", opts.Image)
	}
	if opts.MemoryLimit != "4Gi" || opts.CPULimit != "2" || opts.Storage != "20Gi" {
		t.Fatalf("sizing = %q / %q / %q", opts.CPULimit, opts.MemoryLimit, opts.Storage)
	}
	if opts.ListenPort != 7422 || opts.RunAsUser != 501 || opts.PidsLimit != 1024 {
		t.Fatalf("numeric defaults = %d / %d / %d", opts.ListenPort, opts.RunAsUser, opts.PidsLimit)
	}
	if !opts.Wait || !opts.ReadOnlyRoot {
		t.Fatal("boolean defaults must survive; a deploy that does not wait or writes a mutable root is a different deployment")
	}

	overridden := gitAgentDeployRequest{
		Name: "worker-01", GitAgentDeploymentConfig: GitAgentDeploymentConfig{
			Target: "docker", MemoryLimit: "8Gi", Image: "  ",
		},
	}.options("git-agent")
	if overridden.MemoryLimit != "8Gi" {
		t.Fatalf("explicit memory limit = %q", overridden.MemoryLimit)
	}
	if overridden.Image != "ghcr.io/flanksource/captain:latest" {
		t.Fatalf("a blank override must not blank the default, got %q", overridden.Image)
	}
}

// The agent-login Secret is the difference between a sidecar that can reach a
// model provider and one that enrolls and then fails its first dispatch, so the
// UI's value has to survive the hop into the CLI's options.
func TestDeployRequestCarriesTheCredentialsSecret(t *testing.T) {
	opts := gitAgentDeployRequest{
		Name: "worker-01", GitAgentDeploymentConfig: GitAgentDeploymentConfig{
			Target: "kubernetes", Namespace: "agents",
			CredentialsSecret: "  captain-agent-credentials  ",
		},
	}.options("git-agent")

	if opts.CredentialsSecret != "captain-agent-credentials" {
		t.Fatalf("credentials secret = %q, want it trimmed and carried", opts.CredentialsSecret)
	}
	if describeCredentials(opts) == "" || strings.Contains(describeCredentials(opts), "none declared") {
		t.Fatalf("a deploy with an agent-login Secret still reports no credentials: %q", describeCredentials(opts))
	}
}

// Creating a namespace is the one cluster-scoped change a deploy makes, and it
// outlives an undeploy — so it travels as an explicit intent from the form
// rather than being inferred server-side from a name that is merely absent.
func TestDeployRequestCarriesTheCreateNamespaceIntent(t *testing.T) {
	plain := gitAgentDeployRequest{
		Name: "worker-01", GitAgentDeploymentConfig: GitAgentDeploymentConfig{
			Target: "kubernetes", Namespace: "agents",
		},
	}
	if plain.options("git-agent").CreateNamespace {
		t.Fatal("a namespace was created without being asked for")
	}
	if got := plain.options("git-agent").Namespace; got != "agents" {
		t.Fatalf("namespace = %q", got)
	}

	creating := plain
	creating.CreateNamespace = true
	opts := creating.options("git-agent")
	if !opts.CreateNamespace {
		t.Fatal("the create intent did not reach the deploy options")
	}

	// The dry run has to name it: an operator previewing a deploy should see the
	// change that undeploy will not reverse.
	mutations := deployMutations(deploy.Plan{Name: "w", Backend: "git-agent", Target: deploy.TargetKubernetes}, opts)
	if !slices.ContainsFunc(mutations, func(m string) bool {
		return strings.Contains(m, "create namespace agents")
	}) {
		t.Fatalf("mutations do not mention creating the namespace: %v", mutations)
	}
	if slices.ContainsFunc(deployMutations(deploy.Plan{Name: "w", Backend: "git-agent", Target: deploy.TargetKubernetes},
		plain.options("git-agent")), func(m string) bool {
		return strings.Contains(m, "create namespace")
	}) {
		t.Fatal("a deploy that creates nothing must not say it would")
	}
}

// Undeploy against the wrong runtime removes nothing and reports success,
// leaving a live sidecar on the network holding a valid key and a checkout of
// the source tree. So the target comes from what deploy recorded.
func TestUndeployTargetComesFromTheDeploymentRecord(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	if _, err := resolveUndeployTarget(backend, "worker-01", ""); err == nil ||
		!strings.Contains(err.Error(), "no record of deploying") {
		t.Fatalf("an unrecorded agent must not be guessed at, got %v", err)
	}

	plan := deploy.Plan{Name: "worker-01", Backend: backend, Target: deploy.TargetKubernetes}
	opts := deployOptions(plan.Name)
	opts.Target = string(plan.Target)
	if err := recordDeployment(plan, opts, "captain"); err != nil {
		t.Fatal(err)
	}

	target, err := resolveUndeployTarget(backend, "worker-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if target != deploy.TargetKubernetes {
		t.Fatalf("target = %q, want kubernetes", target)
	}

	// An explicit target that contradicts the record is refused rather than
	// obeyed: obeying it would silently tear down nothing.
	if _, err := resolveUndeployTarget(backend, "worker-01", "docker"); err == nil ||
		!strings.Contains(err.Error(), "was deployed on kubernetes") {
		t.Fatalf("a contradicting target must be refused, got %v", err)
	}
	if _, err := resolveUndeployTarget(backend, "worker-01", "kubernetes"); err != nil {
		t.Fatalf("an agreeing target must be accepted: %v", err)
	}

	recorded, ok := lookupDeployment(backend, "worker-01")
	if !ok || recorded.Namespace != "captain" || recorded.Workload != plan.WorkloadName() {
		t.Fatalf("recorded = %+v, ok = %v", recorded, ok)
	}

	if err := forgetDeployment(backend, "worker-01"); err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupDeployment(backend, "worker-01"); ok {
		t.Fatal("a torn-down deployment must leave no record to offer again")
	}
}

// A workload that has been placed but has not finished joining is invisible in
// the roster otherwise, which leaves an operator with a running sidecar and no
// way to remove it from the UI.
func TestRosterShowsDeployedAgentsBeforeTheyEnroll(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	plan := deploy.Plan{Name: "worker-09", Backend: backend, Target: deploy.TargetDocker}
	if err := recordDeployment(plan, deployOptions(plan.Name), ""); err != nil {
		t.Fatal(err)
	}

	res, err := RunGitAgentList(GitAgentListOptions{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	entries := res.([]GitAgentListEntry)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Name != "worker-09" || !strings.Contains(entries[0].Status, "waiting to enroll") {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[0].Deployment == nil || entries[0].Deployment.Target != string(deploy.TargetDocker) {
		t.Fatalf("the roster must carry the runtime so the UI can offer to tear it down: %+v", entries[0])
	}

	// Once it enrolls it appears once, not twice, and keeps its deployment.
	dir := gitAgentDirectory{backend: backend}
	if err := dir.RecordAgent(gitAgentEnrollmentFor("worker-09")); err != nil {
		t.Fatal(err)
	}
	res, err = RunGitAgentList(GitAgentListOptions{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	entries = res.([]GitAgentListEntry)
	if len(entries) != 1 {
		t.Fatalf("an enrolled deployment must not be listed twice: %+v", entries)
	}
	if entries[0].Status != "enrolled" || entries[0].Deployment == nil {
		t.Fatalf("entry = %+v", entries[0])
	}
}

// An agent captain did not place has no recorded runtime, so the UI must not be
// told it can tear it down.
func TestRosterOmitsDeploymentForASelfManagedAgent(t *testing.T) {
	isolatedConfig(t)
	const backend = "git-agent"

	dir := gitAgentDirectory{backend: backend}
	if err := dir.RecordAgent(gitAgentEnrollmentFor("worker-01")); err != nil {
		t.Fatal(err)
	}
	res, err := RunGitAgentList(GitAgentListOptions{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	entries := res.([]GitAgentListEntry)
	if len(entries) != 1 || entries[0].Deployment != nil {
		t.Fatalf("entries = %+v", entries)
	}

	// And the config carries no deployments block at all, so nothing downstream
	// can mistake an absent record for an empty one.
	cfg, _, err := captainconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cfg.Sandbox.Backends[backend].Options["deployments"]; exists {
		t.Fatal("a self-managed enrollment must not create a deployments block")
	}
}
