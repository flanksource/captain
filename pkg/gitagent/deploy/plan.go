// Package deploy places a git-agent sidecar onto a container runtime.
//
// A sidecar is the machine that executes dispatched work. It runs
// `captain sandbox git-agent serve --role sidecar`, which clones the dispatched
// tree, runs a coding agent over it, and pushes the result back. Everything it
// executes is agent-authored, and the run itself is unsandboxed inside the
// workload (pkg/cli/gitagent_runtask.go pins Sandbox "none"), so the container
// or pod IS the containment boundary — there is no inner one to fall back on.
// That is why sizing and security are first-class inputs here rather than
// deployment trivia.
//
// The package is split so that everything decidable without touching a runtime
// stays pure and testable: Plan below is target-neutral, security.go holds the
// refusals, and docker.go / kubernetes*.go render and apply it.
package deploy

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// sortedKeys gives map iteration a stable order, so a rendered argv or object
// set is byte-comparable against a golden value in tests.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Target names a container runtime a sidecar can be placed on.
type Target string

const (
	TargetDocker     Target = "docker"
	TargetKubernetes Target = "kubernetes"
)

// ParseTarget validates the selector, naming the alternatives. There is
// deliberately no default and no auto-detection: on a host with both a Docker
// daemon and a kubeconfig, guessing would silently pick where an agent with
// access to the source tree ends up running.
func ParseTarget(value string) (Target, error) {
	switch target := Target(strings.ToLower(strings.TrimSpace(value))); target {
	case TargetDocker, TargetKubernetes:
		return target, nil
	case "":
		return "", fmt.Errorf("--target is required; want one of: %s, %s", TargetDocker, TargetKubernetes)
	default:
		return "", fmt.Errorf("invalid --target %q; want one of: %s, %s", value, TargetDocker, TargetKubernetes)
	}
}

// Sizing is the resource envelope, expressed in Kubernetes quantity notation
// for both targets so there is one input language. Docker's flags take bytes
// and fractional CPUs, which Quantity converts to exactly; going the other way
// (Docker notation into a k8s manifest) does not round-trip.
type Sizing struct {
	CPURequest    resource.Quantity
	CPULimit      resource.Quantity
	MemoryRequest resource.Quantity
	MemoryLimit   resource.Quantity
	Storage       resource.Quantity
	TmpSize       resource.Quantity
	PidsLimit     int
}

// DockerCPUs renders the CPU limit for `docker run --cpus`.
func (s Sizing) DockerCPUs() string {
	return fmt.Sprintf("%g", s.CPULimit.AsApproximateFloat64())
}

// DockerMemoryBytes renders the memory limit for `docker run --memory`.
func (s Sizing) DockerMemoryBytes() string { return fmt.Sprintf("%d", s.MemoryLimit.Value()) }

// DockerMemoryReservationBytes renders the request for `--memory-reservation`.
// There is no docker equivalent of a CPU *request*: `--cpu-shares` is a
// relative weight under contention, not a reservation, so mapping a quantity
// onto it would report a guarantee the runtime does not make.
func (s Sizing) DockerMemoryReservationBytes() string {
	return fmt.Sprintf("%d", s.MemoryRequest.Value())
}

// DockerTmpfsSize renders the /tmp size for `--tmpfs`.
func (s Sizing) DockerTmpfsSize() string { return fmt.Sprintf("%d", s.TmpSize.Value()) }

// SizingRequest is the unparsed form, straight off the CLI flags.
type SizingRequest struct {
	CPURequest    string
	CPULimit      string
	MemoryRequest string
	MemoryLimit   string
	Storage       string
	TmpSize       string
	PidsLimit     int
}

// ParseSizing validates every quantity up front, naming the flag that is wrong.
// resource.ParseQuantity rejects "4GB" and "2.5.1" that a hand-rolled parser
// would accept, and a bad value discovered at apply time would already have
// burned a single-use join token.
func ParseSizing(request SizingRequest) (Sizing, error) {
	sizing := Sizing{PidsLimit: request.PidsLimit}
	for _, field := range []struct {
		flag   string
		raw    string
		into   *resource.Quantity
		wanted string
	}{
		{"--cpu-request", request.CPURequest, &sizing.CPURequest, "a CPU quantity such as 500m or 2"},
		{"--cpu-limit", request.CPULimit, &sizing.CPULimit, "a CPU quantity such as 500m or 2"},
		{"--memory-request", request.MemoryRequest, &sizing.MemoryRequest, "a memory quantity such as 1Gi"},
		{"--memory-limit", request.MemoryLimit, &sizing.MemoryLimit, "a memory quantity such as 4Gi"},
		{"--storage", request.Storage, &sizing.Storage, "a storage quantity such as 20Gi"},
		{"--tmp-size", request.TmpSize, &sizing.TmpSize, "a memory quantity such as 1Gi"},
	} {
		quantity, err := resource.ParseQuantity(strings.TrimSpace(field.raw))
		if err != nil {
			return Sizing{}, fmt.Errorf("%s %q is not %s", field.flag, field.raw, field.wanted)
		}
		if quantity.Sign() <= 0 {
			return Sizing{}, fmt.Errorf("%s must be greater than zero, got %q", field.flag, field.raw)
		}
		*field.into = quantity
	}
	if sizing.PidsLimit < 0 {
		return Sizing{}, fmt.Errorf("--pids-limit must not be negative, got %d", sizing.PidsLimit)
	}
	if sizing.CPULimit.Cmp(sizing.CPURequest) < 0 {
		return Sizing{}, fmt.Errorf("--cpu-limit %s is below --cpu-request %s",
			sizing.CPULimit.String(), sizing.CPURequest.String())
	}
	if sizing.MemoryLimit.Cmp(sizing.MemoryRequest) < 0 {
		return Sizing{}, fmt.Errorf("--memory-limit %s is below --memory-request %s",
			sizing.MemoryLimit.String(), sizing.MemoryRequest.String())
	}
	return sizing, nil
}

// Plan is everything needed to place one sidecar, resolved and validated, with
// no runtime touched yet. Both renderers consume it; --dry-run prints it.
//
// It deliberately does NOT carry the join token. The token reaches the workload
// through a file (see JoinPath), never through this struct, so no rendering of
// a Plan can leak it into argv, a pod spec, or a log line.
type Plan struct {
	Name    string
	Backend string
	Target  Target
	Image   string

	// Home is where the state volume mounts. It must be the image's own home
	// directory for its user: a Docker named volume is initialized from image
	// content including ownership, so an invented path is created root-owned and
	// the unprivileged process cannot write the keys it generates on first start.
	Home string

	// ListenPort is the port the sidecar serves git-receive-pack on inside the
	// workload; HostPort is the docker-published port on the loopback interface.
	ListenPort int
	HostPort   int

	// Supervisor is the address the sidecar reaches the mailbox on, Advertise the
	// address the supervisor dispatches back to. Both are resolved by detection
	// and always passed explicitly — left unset, the receiver derives the agent's
	// address from the connection source, which for a pod or a Docker Desktop VM
	// is an address the supervisor cannot route to. The agent still enrolls, so
	// the failure surfaces only at first dispatch.
	Supervisor      string
	Advertise       string
	HostFingerprint string

	// Transport is the protocol the sidecar's own receive endpoint speaks. It
	// must agree with Advertise's scheme: the supervisor dispatches to that URL,
	// and a workload serving the other protocol would accept the connection and
	// fail the handshake. Empty means ssh, which is what a docker sidecar on a
	// published loopback port serves.
	Transport string

	// JoinPath is where the token file is mounted inside the workload.
	JoinPath string

	// CredentialsSecret names an existing Secret holding the redacted agent
	// logins that pkg/credsync keeps fresh. Empty leaves the workload without
	// one, which is the previous behaviour.
	CredentialsSecret string
	// CredentialsDir is the host directory a Docker workload bind-mounts for the
	// same purpose — credsync's DirectoryTarget path.
	CredentialsDir string

	// ExternalRoute is set only for the externally-routed topology; see its own
	// type. The zero value means no route is rendered.
	ExternalRoute ExternalRoute

	Sizing   Sizing
	Security Security

	// EnvNames are forwarded by NAME only; values are read from the deploying
	// process, so a credential never enters argv.
	EnvNames       []string
	EnvFromSecrets []string
}

// WorkloadName is the container / object name. The agent name is already
// constrained to a DNS label by gitagent.ValidateTaskID at enrollment, so this
// is safe as both a container name and a Kubernetes object name.
func (p Plan) WorkloadName() string { return "captain-git-agent-" + p.Name }

// VolumeName is the docker named volume / Kubernetes PVC holding agent state.
func (p Plan) VolumeName() string { return p.WorkloadName() + "-state" }

// JoinSecretName is the Kubernetes Secret carrying the single-use join token.
func (p Plan) JoinSecretName() string { return p.WorkloadName() + "-join" }

// Labels identify everything this package creates, so teardown can find the
// workload by selector even if it was renamed.
func (p Plan) Labels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":          "captain-git-agent",
		"app.kubernetes.io/instance":      p.Name,
		"app.kubernetes.io/managed-by":    "captain",
		"captain.flanksource.com/backend": p.Backend,
	}
}

// ServeArgs is the argv the workload runs, after the entrypoint. It is
// token-free by construction: the token arrives via --token-file, because argv
// is visible in `docker inspect`, in a pod spec, and in /proc/<pid>/cmdline.
func (p Plan) ServeArgs() []string {
	args := []string{
		"sandbox", "git-agent", "serve",
		"--role", "sidecar",
		"--transport", p.serveTransport(),
		"--backend", p.Backend,
		"--listen", fmt.Sprintf("0.0.0.0:%d", p.ListenPort),
		"--advertise", p.Advertise,
		"--supervisor", p.Supervisor,
		"--host-fingerprint", p.HostFingerprint,
	}
	if p.JoinPath != "" {
		args = append(args, "--token-file", p.JoinPath)
	}
	if p.HasExternalRoute() {
		args = append(args,
			"--tls-cert", routeTLSMountPath+"/tls.crt",
			"--tls-key", routeTLSMountPath+"/tls.key")
	}
	return args
}

// serveTransport is the protocol the workload serves, defaulting to ssh so a
// plan that predates the field renders the argv it always did.
func (p Plan) serveTransport() string {
	if p.Transport == "" {
		return "ssh"
	}
	return p.Transport
}

// ExternalRoute is how a supervisor OUTSIDE the cluster reaches the sidecar.
//
// The zero value means there is none, which is not a degraded mode but the
// other supported topology: a supervisor that is itself a pod dispatches to
// captain-git-agent-x.ns.svc.cluster.local over ssh, and there is nothing for an
// ingress controller to front. Rendering is kubernetes_ingress.go's job —
// nothing here knows the word "Ingress".
type ExternalRoute struct {
	// Host is the fully resolved name the supervisor dials. It is resolved on
	// the CLI side rather than derived here, because spec.rules[0].host and the
	// advertise URL must be the same string, and computing it twice is how the
	// two come to differ.
	Host string

	// ClassName selects the controller. Never empty: an Ingress naming no class
	// falls to whichever IngressClass carries the default annotation, and the
	// wrong controller has none of the settings this transport depends on.
	ClassName string

	// ClusterIssuer names the cert-manager ClusterIssuer that mints the
	// certificate for Host. Mutually exclusive with TLSSecret.
	ClusterIssuer string
	// TLSSecret names a certificate the operator already holds, for a cluster
	// with no cert-manager or a pre-issued wildcard.
	TLSSecret string

	// Annotations are merged over the controller defaults, last write wins, so an
	// operator can raise a timeout or state a non-nginx equivalent.
	Annotations map[string]string
}

// HasExternalRoute reports which of the two supported topologies this is.
func (p Plan) HasExternalRoute() bool { return p.ExternalRoute.Host != "" }

// IngressName is the route object.
//
// It is WorkloadName, exactly like the Service and the Deployment, because
// undeploy reconstructs a Plan from a name, a backend and a target and nothing
// else — so deriving every route name from the workload name is what lets it
// delete the route without knowing one was ever rendered.
func (p Plan) IngressName() string { return p.WorkloadName() }

// IngressTLSSecretName is where the certificate for Host lives.
//
// An operator-supplied Secret deliberately falls outside the derived name:
// teardown deletes what it derives, and removing a shared wildcard would take
// every other agent on that domain offline.
func (p Plan) IngressTLSSecretName() string {
	if p.ExternalRoute.TLSSecret != "" {
		return p.ExternalRoute.TLSSecret
	}
	return p.WorkloadName() + "-tls"
}
