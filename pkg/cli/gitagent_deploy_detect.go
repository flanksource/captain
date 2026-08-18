// Address detection for `captain sandbox git-agent deploy`.
//
// A git-agent topology needs two addresses pointing in opposite directions, and
// getting either wrong produces the same symptom: enrollment succeeds, the
// roster looks healthy, and the first dispatch — minutes or hours later — fails.
// That is why everything here proves rather than guesses, and refuses rather
// than defaults.
package cli

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

// serviceAccountTokenPath is the projected token every in-cluster pod gets. Its
// presence alongside the service env vars is what client-go itself treats as
// proof of running in a cluster.
const serviceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// detectedMailbox is a mailbox this host serves, proven live and proven to be
// this host's own.
type detectedMailbox struct {
	// Transport is the channel the sidecar relays over, and the scheme of the
	// URL it is given.
	Transport mailboxTransport
	// Listen is the recorded bind address, Port the port parsed out of it.
	Listen string
	Port   int
	// HostFingerprint is the identity the mailbox presented: an SSH host key
	// over ssh, a TLS public-key pin over https. The sidecar pins it, and the
	// two are interchangeable everywhere downstream because both are compared
	// against what the endpoint actually presents.
	HostFingerprint string
	// OffHostAddresses is every non-loopback address of this host that answered
	// as this same mailbox, best-ranked first. Empty when the caller did not
	// need the proof.
	//
	// It is evidence that the mailbox answers off loopback — not proof of the
	// path the sidecar takes, which for docker is the host.docker.internal
	// alias and not any of these.
	OffHostAddresses []string
}

// mailboxDetection asks for a mailbox with the properties a given deploy needs.
type mailboxDetection struct {
	Backend string
	// NeedOffHost requires proof that a workload in another network namespace
	// can reach the mailbox, not merely that this host can.
	NeedOffHost bool
	// Transport forces one channel when this host serves both. Empty picks.
	Transport mailboxTransport
}

// detectMailbox proves this host serves a live git-agent mailbox before any
// token is minted.
//
// The record is the only authoritative source: a serving process writes it on
// startup, and a sidecar taking over the same address clears it. Falling back to
// a hardcoded :7422 would reintroduce the guess this exists to remove.
func detectMailbox(ctx context.Context, req mailboxDetection) (detectedMailbox, error) {
	record, err := selectMailboxRecord(req.Backend, req.Transport)
	if err != nil {
		return detectedMailbox{}, err
	}
	if err := refuseUnusableMailbox(record, req.NeedOffHost); err != nil {
		return detectedMailbox{}, err
	}
	port, err := record.Port()
	if err != nil {
		return detectedMailbox{}, err
	}
	identity, err := expectedMailboxIdentity(record)
	if err != nil {
		return detectedMailbox{}, err
	}
	local, err := record.LoopbackURL()
	if err != nil {
		return detectedMailbox{}, err
	}
	if err := gitagent.VerifyEndpointIdentity(ctx, local, identity); err != nil {
		return detectedMailbox{}, fmt.Errorf("no live git-agent mailbox on %s: %w\n%s",
			local, err, startMailboxHint(record))
	}

	detected := detectedMailbox{
		Transport: record.Transport, Listen: record.Listen, Port: port, HostFingerprint: identity,
	}
	if !req.NeedOffHost {
		return detected, nil
	}
	// Binding off-loopback is not the same as being reachable off-loopback: a
	// host firewall can accept on 127.0.0.1 and drop everything else.
	reachable, err := proveOffHostReach(ctx, record, detected)
	if err != nil {
		return detectedMailbox{}, err
	}
	detected.OffHostAddresses = reachable
	return detected, nil
}

// selectMailboxRecord picks which recorded mailbox a deploy enrolls against.
//
// HTTPS is preferred when it is usable: it is what `captain serve` hosts, so it
// needs no second long-lived process. A usable ssh mailbox beats an https one
// that is recorded but serving plain HTTP, because it works — the caller only
// hears about the unusable https record when it is the only one there.
func selectMailboxRecord(backendName string, want mailboxTransport) (mailboxRecord, error) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return mailboxRecord{}, err
	}
	backend, ok := cfg.Sandbox.Backends[backendName]
	if !ok {
		return mailboxRecord{}, fmt.Errorf("backend %q is not configured; %s", backendName, noMailboxHint)
	}
	records := mailboxRecords(backend.Options)
	if want != "" {
		record, ok := records[want]
		if !ok {
			return mailboxRecord{}, fmt.Errorf(
				"no %s mailbox has served from backend %q on this host (recorded: %s); %s",
				want, backendName, recordedTransports(records), noMailboxHint)
		}
		return record, nil
	}
	if record, ok := records[transportHTTPS]; ok && record.Encrypted {
		return record, nil
	}
	if record, ok := records[transportSSH]; ok {
		return record, nil
	}
	if record, ok := records[transportHTTPS]; ok {
		return record, nil // unusable, and refuseUnusableMailbox says exactly why
	}
	return mailboxRecord{}, fmt.Errorf(
		"no mailbox has served from backend %q on this host, so there is no address to enroll against; %s",
		backendName, noMailboxHint)
}

// noMailboxHint names both ways to make this host a supervisor. It is one
// string because every refusal that ends here needs the same two commands.
const noMailboxHint = "either run `captain serve --host 0.0.0.0 --tls --tls-host <address agents dial>`, " +
	"which hosts the mailbox over https, or run `captain sandbox git-agent serve --role mailbox` for the ssh transport"

func recordedTransports(records map[mailboxTransport]mailboxRecord) string {
	if len(records) == 0 {
		return "none"
	}
	names := make([]string, 0, len(records))
	for transport := range records {
		names = append(names, string(transport))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// refuseUnusableMailbox reports why a recorded mailbox cannot serve a deployed
// agent, naming the flags that fix it.
func refuseUnusableMailbox(record mailboxRecord, needOffHost bool) error {
	if !record.Encrypted {
		return fmt.Errorf(
			"`captain serve` is hosting the mailbox on %s over plain HTTP, and an agent's captain token would "+
				"cross the network in clear text; restart it with: "+
				"captain serve --host 0.0.0.0 --tls --tls-host <address agents dial>", record.Listen)
	}
	host, _, err := net.SplitHostPort(record.Listen)
	if err != nil {
		return fmt.Errorf("recorded mailbox listen address %q is not [host]:port: %w", record.Listen, err)
	}
	// A loopback-bound mailbox can never be reached from another network
	// namespace, so no workload could ever relay to it. Cheapest check, and the
	// most common misconfiguration.
	if !needOffHost || !isLoopbackHost(host) {
		return nil
	}
	if record.Transport == transportHTTPS {
		return fmt.Errorf(
			"`captain serve` is hosting the mailbox on %s, which no container or pod can reach; "+
				"restart it with --host 0.0.0.0", record.Listen)
	}
	port, err := record.Port()
	if err != nil {
		return err
	}
	return fmt.Errorf(
		"the mailbox is bound to %s, which no container or pod can reach; restart it with --listen :%d",
		record.Listen, port)
}

// expectedMailboxIdentity is what the endpoint must present to be this host's
// own mailbox.
//
// Over ssh it comes from the local host key rather than the record: the key file
// is always there and proves the listener holds *this host's* identity, which is
// stronger than agreeing with something written beside it. Over https the served
// certificate may be one supplied with --tls-cert and not in the keys directory,
// so the record — written by the process that is serving it — is the only source
// that is guaranteed to name the right one.
func expectedMailboxIdentity(record mailboxRecord) (string, error) {
	if record.Transport == transportHTTPS {
		if record.Identity == "" {
			return "", fmt.Errorf(
				"the recorded https mailbox on %s has no certificate pin; restart `captain serve` to re-record it",
				record.Listen)
		}
		return record.Identity, nil
	}
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return "", err
	}
	_, fingerprint, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, hostKeyName))
	return fingerprint, err
}

// verifySupervisorNameIsCovered proves the mailbox's certificate covers the
// name the deployed agent will dial.
//
// The agent verifies that name against a certificate it has not seen yet, from
// a network namespace where the name resolves and this one where it may not.
// Reading it from the endpoint here — the same endpoint detection just pinned —
// is the only way to be sure before the workload exists.
func verifySupervisorNameIsCovered(ctx context.Context, mailbox detectedMailbox, supervisor string) error {
	if mailbox.Transport != transportHTTPS {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(supervisor))
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("supervisor address %q must be https://host[:port]", supervisor)
	}
	local, err := mailboxLoopbackURL(mailbox)
	if err != nil {
		return err
	}
	return gitagent.VerifyEndpointCoversName(ctx, local, parsed.Hostname())
}

// mailboxLoopbackURL is how this host reaches the mailbox it detected.
func mailboxLoopbackURL(mailbox detectedMailbox) (string, error) {
	return mailboxRecord{Transport: mailbox.Transport, Listen: mailbox.Listen}.LoopbackURL()
}

func startMailboxHint(record mailboxRecord) string {
	if record.Transport == transportHTTPS {
		return "start one with: captain serve --host 0.0.0.0 --tls --tls-host <address agents dial>"
	}
	return "start one with: captain sandbox git-agent serve --role mailbox --listen " + record.Listen
}

// isLoopbackHost reports whether a bind host reaches only this host. An empty
// host means "all interfaces", which is what `:7422` yields.
func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

// runningInCluster reports whether this process is itself a pod. Mirrors the
// test client-go uses, so the answer agrees with what the Kubernetes client
// will do when it loads its own config.
func runningInCluster() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" || os.Getenv("KUBERNETES_SERVICE_PORT") == "" {
		return false
	}
	info, err := os.Stat(serviceAccountTokenPath)
	return err == nil && info.Mode().IsRegular()
}

// resolveSupervisorAddress returns the address the DEPLOYED agent uses to reach
// this mailbox, and where that address came from.
func resolveSupervisorAddress(target deploy.Target, mailbox detectedMailbox, override string) (address, source string, err error) {
	if given := strings.TrimSpace(override); given != "" {
		return normalizeSupervisorAddress(given, mailbox.Port), "flag", nil
	}
	switch target {
	case deploy.TargetDocker:
		// host-gateway resolves to the bridge gateway on Linux and is built in on
		// Docker Desktop, so one argv covers both and there is no platform branch
		// to get wrong. Over https it must also be a name the certificate covers,
		// which is why tlsSubjectNames includes it.
		if mailbox.Transport == transportHTTPS {
			return fmt.Sprintf("https://host.docker.internal:%d", mailbox.Port), "docker-host-gateway", nil
		}
		return fmt.Sprintf("ssh://captain@host.docker.internal:%d", mailbox.Port), "docker-host-gateway", nil
	case deploy.TargetKubernetes:
		if runningInCluster() {
			return "", "", fmt.Errorf(
				"captain is running in-cluster but cannot name the Service that fronts its own mailbox; "+
					"pass --supervisor-address %s://<service>.<namespace>.svc.cluster.local:%d",
				mailbox.Transport, mailbox.Port)
		}
		// Guessing the LAN address here would produce a pod that CrashLoops on
		// enroll, holding a credential nothing revoked.
		return "", "", fmt.Errorf(
			"captain is not running in the target cluster, so no route back to this host can be proven; "+
				"pass --supervisor-address with an address the cluster can reach (this host answers on %s, "+
				"which is usually NOT reachable from a managed cluster)",
			mailboxEndpointList(mailbox))
	}
	return "", "", fmt.Errorf("unsupported target %q", target)
}

// resolveAdvertiseAddress returns the address the SUPERVISOR dispatches to.
//
// It is always set explicitly. Left empty, the receiver derives it from the
// connection's source address (pkg/gitagent/server.go), which for a pod is a
// pod IP and for Docker Desktop is a VM-internal address — neither routable
// from the supervisor, and neither detectable as wrong until a dispatch fails.
func resolveAdvertiseAddress(
	target deploy.Target, plan deploy.Plan, namespace, override string, inCluster bool,
) (address, source string, err error) {
	if given := strings.TrimSpace(override); given != "" {
		address, err := advertiseURL(given)
		return address, "flag", err
	}
	switch target {
	case deploy.TargetDocker:
		if plan.HostPort == 0 {
			return "", "", fmt.Errorf("docker deployment needs a published host port before it can advertise")
		}
		address, err := advertiseURL(fmt.Sprintf("captain@127.0.0.1:%d", plan.HostPort))
		return address, "docker-published-port", err
	case deploy.TargetKubernetes:
		if plan.HasExternalRoute() {
			// Joined by the transport's own helper rather than formatted here, so
			// the Ingress routing /git and the advertise URL cannot disagree.
			address, err := gitagent.HTTPSRepoURL("https://"+plan.ExternalRoute.Host, SidecarRepoName)
			return address, "cluster-ingress", err
		}
		if !inCluster {
			return "", "", fmt.Errorf(
				"captain is not running in the target cluster, so a ClusterIP address it cannot route to is "+
					"the only thing left to advertise — the agent would enroll, look healthy, and never "+
					"receive a dispatch. Pass --domain <dns domain the cluster's ingress controller serves> "+
					"to publish this agent at %s.<domain>, or --advertise with a route you manage yourself",
				plan.Name)
		}
		address, err := advertiseURL(fmt.Sprintf("captain@%s.%s.svc.cluster.local:%d",
			plan.WorkloadName(), namespace, plan.ListenPort))
		return address, "cluster-service", err
	}
	return "", "", fmt.Errorf("unsupported target %q", target)
}

// normalizeSupervisorAddress adds the scheme and, critically, a port.
//
// A portless endpoint defaults to :22 over ssh and :443 over https, so
// `--supervisor-address ssh://host` would otherwise silently probe sshd and
// `https://host` an unrelated web server, instead of the mailbox. A schemeless
// address is ssh, which is the form written before HTTPS existed.
func normalizeSupervisorAddress(address string, defaultPort int) string {
	normalized := strings.TrimSuffix(strings.TrimSpace(address), "/")
	if !strings.Contains(normalized, "://") {
		normalized = "ssh://" + normalized
	}
	scheme, hostPort, _ := strings.Cut(normalized, "://")
	if _, _, err := net.SplitHostPort(hostPort); err != nil {
		return fmt.Sprintf("%s://%s:%d", scheme, hostPort, defaultPort)
	}
	return normalized
}

// freeLoopbackPort reserves a port by binding and releasing it.
//
// The port has to be known before `docker run`, because the sidecar performs
// its join at startup and the join carries the advertise URL — so reading the
// port back from `docker port` afterwards would be too late. The window between
// release and bind is a real race; docker fails loudly if it is lost, and
// --host-port bypasses it.
func freeLoopbackPort(after int) (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve a host port for the sidecar: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if port == after {
		return 0, fmt.Errorf("reserved port %d collides with the mailbox; retry or pass --host-port", port)
	}
	return port, nil
}
