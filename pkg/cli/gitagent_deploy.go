// `captain sandbox git-agent deploy` — enroll an agent and place its sidecar.
//
// The command exists because the gap between `add` and a working agent is where
// this topology goes wrong. `add` prints a join command; carrying it to a
// machine by hand means choosing an image, a resource envelope, a security
// posture, and — the part that fails silently — the two addresses the protocol
// needs pointing in opposite directions. Every step here either proves its
// input or refuses.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/captain/pkg/gitagent/deploy"
	"github.com/flanksource/clicky"
)

type GitAgentDeployOptions struct {
	Name    string `args:"true" help:"Name for the agent being enrolled and deployed"`
	Backend string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
	Target  string `flag:"target" help:"Where the sidecar runs: docker or kubernetes"`
	// Transport is only needed when this host serves both; otherwise detection
	// takes the one that is there.
	Transport string `flag:"transport" help:"Which mailbox to enroll against when this host serves both: https (captain serve) or ssh"`

	Namespace       string `flag:"namespace" help:"kubernetes: namespace for the sidecar (default: the kubeconfig context's)"`
	CreateNamespace bool   `flag:"create-namespace" help:"kubernetes: create --namespace when it does not exist, instead of refusing"`
	KubeContext     string `flag:"kube-context" help:"kubernetes: kubeconfig context to apply into (default: current-context)"`

	Domain            string   `flag:"domain" help:"kubernetes: DNS domain a supervisor outside the cluster reaches agents on; this agent is published at <name>.<domain> behind an Ingress"`
	IngressClass      string   `flag:"ingress-class" help:"kubernetes: ingressClassName of the controller serving --domain" default:"nginx"`
	IngressIssuer     string   `flag:"ingress-issuer" help:"kubernetes: cert-manager ClusterIssuer that mints the certificate for <name>.<domain>"`
	IngressTLSSecret  string   `flag:"ingress-tls-secret" help:"kubernetes: existing TLS Secret covering <name>.<domain>, instead of --ingress-issuer"`
	IngressAnnotation []string `flag:"ingress-annotation" help:"kubernetes: extra key=value Ingress annotations, merged over the ingress-nginx defaults; required for any other controller, and where a source-range allowlist goes"`

	Image           string `flag:"image" help:"Sidecar image; must carry the captain binary on PATH" default:"ghcr.io/flanksource/captain:latest"`
	ImagePullPolicy string `flag:"image-pull-policy" help:"kubernetes: Always, IfNotPresent or Never" default:"IfNotPresent"`
	ImagePullSecret string `flag:"image-pull-secret" help:"kubernetes: name of an existing imagePullSecret"`

	SupervisorAddress string `flag:"supervisor-address" help:"ssh:// or https:// endpoint the deployed agent uses to reach this mailbox (detected for docker; required for kubernetes)"`
	Advertise         string `flag:"advertise" help:"ssh://host:port the supervisor dispatches back to (detected per target)"`
	ListenPort        int    `flag:"listen-port" help:"Port the sidecar listens on inside the workload" default:"7422"`
	HostPort          int    `flag:"host-port" help:"docker: loopback port published for the sidecar; 0 reserves a free one"`

	CPURequest    string `flag:"cpu-request" help:"kubernetes requests.cpu" default:"500m"`
	CPULimit      string `flag:"cpu-limit" help:"kubernetes limits.cpu, docker --cpus" default:"2"`
	MemoryRequest string `flag:"memory-request" help:"kubernetes requests.memory, docker --memory-reservation" default:"1Gi"`
	MemoryLimit   string `flag:"memory-limit" help:"kubernetes limits.memory, docker --memory" default:"4Gi"`
	Storage       string `flag:"storage" help:"Persistent volume holding HOME: agent key, config and served repos" default:"20Gi"`
	StorageClass  string `flag:"storage-class" help:"kubernetes: StorageClass for the state volume (default: the cluster's)"`
	TmpSize       string `flag:"tmp-size" help:"Writable /tmp; RAM-backed on docker, so it counts against --memory-limit" default:"1Gi"`
	PidsLimit     int    `flag:"pids-limit" help:"Maximum process count in the workload; 0 disables" default:"1024"`

	RunAsUser    int      `flag:"run-as-user" help:"UID to run as; must own --home in the image" default:"501"`
	RunAsGroup   int      `flag:"run-as-group" help:"GID to run as" default:"20"`
	Home         string   `flag:"home" help:"HOME inside the workload; the state volume mounts here" default:"/home/claude"`
	ReadOnlyRoot bool     `flag:"read-only-root" help:"Mount the image root read-only; state on the volume, scratch on /tmp" default:"true"`
	Network      string   `flag:"network" help:"docker: network to attach; host and none are refused" default:"bridge"`
	CapAdd       []string `flag:"cap-add" help:"Linux capabilities to restore on top of an otherwise empty set"`

	Env           []string `flag:"env" help:"Environment variable NAMES to forward; values are read from this process, never argv"`
	EnvFromSecret []string `flag:"env-from-secret" help:"kubernetes: existing Secret names exposed to the sidecar via envFrom"`

	CredentialsSecret string `flag:"credentials-secret" help:"kubernetes: Secret of redacted agent logins kept fresh by 'captain sandbox credentials'; mounted at /run/captain/credentials"`
	CredentialsDir    string `flag:"credentials-dir" help:"docker: host directory of redacted agent logins to bind-mount read-only at /run/captain/credentials"`

	Wait    bool   `flag:"wait" help:"Wait for readiness AND for the enrollment to be recorded here" default:"true"`
	Timeout string `flag:"timeout" help:"How long --wait waits" default:"5m"`
	Replace bool   `flag:"replace" help:"Replace an existing deployment of this name"`
	DryRun  bool   `flag:"dry-run" help:"Print every intended mutation without touching anything" short:"n"`

	reuseEnrollment bool
}

// GitAgentDeployResult reports what was placed and, as importantly, what could
// not be proven: an operator who does not know the sidecar has unrestricted
// egress or no model credentials will find out at the first dispatch.
type GitAgentDeployResult struct {
	Backend string `json:"backend" pretty:"label=Backend"`
	Agent   string `json:"agent" pretty:"label=Agent"`
	Target  string `json:"target" pretty:"label=Target"`
	Image   string `json:"image" pretty:"label=Image"`

	Workload  string   `json:"workload" pretty:"label=Workload"`
	Namespace string   `json:"namespace,omitempty" pretty:"label=Namespace"`
	Objects   []string `json:"objects,omitempty" pretty:"label=Objects"`
	Volume    string   `json:"volume" pretty:"label=State volume"`

	Supervisor      string `json:"supervisor" pretty:"label=Reaches mailbox at"`
	SupervisorFrom  string `json:"supervisorFrom" pretty:"label=Detected from"`
	Advertise       string `json:"advertise" pretty:"label=Dispatched to at"`
	AdvertiseFrom   string `json:"advertiseFrom" pretty:"label=Detected from"`
	HostFingerprint string `json:"hostFingerprint" pretty:"label=Mailbox host key"`
	// OffHostAddresses is every address of this host that answered as the
	// mailbox. It is evidence the mailbox is reachable off loopback — NOT proof
	// of the path the sidecar dials, which is the host.docker.internal alias.
	// Empty when --supervisor-address skipped the proof.
	OffHostAddresses []string `json:"offHostAddresses,omitempty" pretty:"label=Off-loopback proof"`

	// Route is the external hostname a supervisor outside the cluster dispatches
	// to, empty for the in-cluster topology. Reported apart from Advertise
	// because the DNS record pointing it at the controller is the one thing this
	// deploy cannot create and cannot prove.
	Route      string `json:"route,omitempty" pretty:"label=Ingress host"`
	RouteClass string `json:"routeClass,omitempty" pretty:"label=Ingress class"`

	Security    string `json:"security" pretty:"label=Security"`
	Credentials string `json:"credentials" pretty:"label=Agent credentials"`
	// EgressRestricted is always false today: the egress credential proxy has no
	// callers, so the sidecar reaches model APIs, git remotes and package
	// registries directly.
	EgressRestricted bool `json:"egressRestricted" pretty:"label=Egress restricted"`

	Enrolled         bool `json:"enrolled" pretty:"label=Enrolled"`
	Ready            bool `json:"ready" pretty:"label=Ready"`
	EnrollmentReused bool `json:"enrollmentReused,omitempty" pretty:"label=Enrollment reused"`
	Replaced         bool `json:"replaced,omitempty" pretty:"label=Replaced"`
	DryRun           bool `json:"dryRun,omitempty" pretty:"label=Dry Run"`

	// Mutations is every change the deploy intends, in order. It is populated on
	// a dry run and is what the CLI prints and the web UI previews — one builder,
	// so the two cannot describe different deployments.
	Mutations []string `json:"mutations,omitempty" pretty:"label=Would do"`
}

func RunGitAgentDeploy(ctx context.Context, opts GitAgentDeployOptions) (any, error) {
	target, err := deploy.ParseTarget(opts.Target)
	if err != nil {
		return nil, err
	}
	if err := gitagent.ValidateTaskID(opts.Name); err != nil {
		return nil, fmt.Errorf("agent name: %w", err)
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(opts.Timeout))
	if err != nil {
		return nil, fmt.Errorf("--timeout %q is not a duration: %w", opts.Timeout, err)
	}
	sizing, err := deploy.ParseSizing(deploy.SizingRequest{
		CPURequest:    opts.CPURequest,
		CPULimit:      opts.CPULimit,
		MemoryRequest: opts.MemoryRequest,
		MemoryLimit:   opts.MemoryLimit,
		Storage:       opts.Storage,
		TmpSize:       opts.TmpSize,
		PidsLimit:     opts.PidsLimit,
	})
	if err != nil {
		return nil, err
	}

	security := deploy.HardenedSecurity()
	security.RunAsUser, security.RunAsGroup = opts.RunAsUser, opts.RunAsGroup
	security.ReadOnlyRoot, security.Network, security.CapAdd = opts.ReadOnlyRoot, opts.Network, opts.CapAdd
	presets, err := backendPresets(opts.Backend)
	if err != nil {
		return nil, err
	}
	if err := deploy.RefuseUnsafe(security, opts.Home, nil, presets); err != nil {
		return nil, err
	}

	// RecordAgent overwrites an existing entry wholesale. A managed replacement
	// keeps the state volume and therefore must restart from the identity already
	// persisted there instead of minting another one.
	reuseEnrollment, err := replacementReusesEnrollment(opts)
	if err != nil {
		return nil, err
	}
	opts.reuseEnrollment = opts.reuseEnrollment || reuseEnrollment

	transport, err := parseMailboxTransport(opts.Transport)
	if err != nil {
		return nil, err
	}
	mailbox, supervisor, supervisorFrom, err := resolveDeployMailbox(ctx, target, opts, transport)
	if err != nil {
		return nil, err
	}
	opts.Transport = string(mailbox.Transport)
	joinPath := deploy.JoinMountPath
	if opts.reuseEnrollment {
		joinPath = ""
	}

	plan := deploy.Plan{
		Name:            opts.Name,
		Backend:         opts.Backend,
		Target:          target,
		Image:           opts.Image,
		Home:            opts.Home,
		ListenPort:      opts.ListenPort,
		HostPort:        opts.HostPort,
		Supervisor:      supervisor,
		HostFingerprint: mailbox.HostFingerprint,
		JoinPath:        joinPath,
		Sizing:          sizing,
		Security:        security,
		EnvNames:        opts.Env,
		EnvFromSecrets:  opts.EnvFromSecret,
	}
	if err := applyCredentialMount(&plan, opts, target); err != nil {
		return nil, err
	}
	// Before the advertise address, which the route decides.
	if err := applyExternalRoute(&plan, opts, target); err != nil {
		return nil, err
	}
	if target == deploy.TargetDocker && plan.HostPort == 0 {
		if plan.HostPort, err = freeLoopbackPort(mailbox.Port); err != nil {
			return nil, err
		}
	}

	namespace := strings.TrimSpace(opts.Namespace)
	advertise, advertiseFrom, err := resolveAdvertiseAddress(target, plan, namespace, opts.Advertise, runningInCluster())
	if err != nil {
		return nil, err
	}
	plan.Advertise = advertise

	result := GitAgentDeployResult{
		Backend: opts.Backend, Agent: opts.Name, Target: string(target), Image: opts.Image,
		Workload: plan.WorkloadName(), Namespace: namespace, Volume: plan.VolumeName(),
		Supervisor: supervisor, SupervisorFrom: supervisorFrom,
		Advertise: advertise, AdvertiseFrom: advertiseFrom,
		HostFingerprint:  mailbox.HostFingerprint,
		OffHostAddresses: mailbox.OffHostAddresses,
		Route:            plan.ExternalRoute.Host,
		RouteClass:       plan.ExternalRoute.ClassName,
		Security:         security.Describe(),
		Credentials:      describeCredentials(opts),
	}

	// Apply-by-default is only safe if the operator can see where. Printed
	// unconditionally, not just under --dry-run.
	printResolvedTarget(plan, result, timeout)
	if opts.DryRun {
		result.Mutations = deployMutations(plan, opts)
		printDeployDryRun(result.Mutations)
		result.DryRun = true
		return result, nil
	}
	return runDeploy(ctx, plan, opts, result, timeout)
}

func resolveDeployMailbox(ctx context.Context, target deploy.Target, opts GitAgentDeployOptions,
	transport mailboxTransport) (detectedMailbox, string, string, error) {
	if opts.reuseEnrollment {
		supervisor := strings.TrimSpace(opts.SupervisorAddress)
		if supervisor == "" {
			return detectedMailbox{}, "", "", fmt.Errorf(
				"reuse enrollment for agent %q: saved deployment has no supervisor address", opts.Name)
		}
		return detectedMailbox{Transport: transport}, supervisor, "saved deployment", nil
	}

	// Detection before minting. A mint followed by a failed provision leaves a
	// live credential to revoke, and a wrongly resolved address does not surface
	// until the first dispatch.
	mailbox, err := detectMailbox(ctx, mailboxDetection{
		Backend: opts.Backend, NeedOffHost: opts.SupervisorAddress == "", Transport: transport,
	})
	if err != nil {
		return detectedMailbox{}, "", "", err
	}
	supervisor, from, err := resolveSupervisorAddress(target, mailbox, opts.SupervisorAddress)
	if err != nil {
		return detectedMailbox{}, "", "", err
	}
	if err := verifySupervisorNameIsCovered(ctx, mailbox, supervisor); err != nil {
		return detectedMailbox{}, "", "", err
	}
	return mailbox, supervisor, from, nil
}

// applyCredentialMount puts the agent-login source on the plan, refusing the
// flag that belongs to the other target rather than silently ignoring it — an
// ignored --credentials-dir on a Kubernetes deploy would look configured and
// leave the sidecar with no login.
func applyCredentialMount(plan *deploy.Plan, opts GitAgentDeployOptions, target deploy.Target) error {
	secret := strings.TrimSpace(opts.CredentialsSecret)
	directory := strings.TrimSpace(opts.CredentialsDir)
	switch {
	case secret != "" && directory != "":
		return fmt.Errorf("--credentials-secret and --credentials-dir are mutually exclusive")
	case secret != "" && target != deploy.TargetKubernetes:
		return fmt.Errorf("--credentials-secret needs --target kubernetes; use --credentials-dir for docker")
	case directory != "" && target != deploy.TargetDocker:
		return fmt.Errorf("--credentials-dir needs --target docker; use --credentials-secret for kubernetes")
	}
	if directory != "" {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return fmt.Errorf("resolve --credentials-dir %q: %w", directory, err)
		}
		// A path docker cannot bind-mount produces a container that fails to
		// start, long after deploy has already minted a token.
		if info, err := os.Stat(absolute); err != nil {
			return fmt.Errorf("--credentials-dir %s: %w (run `captain sandbox credentials sync --directory %s` first)",
				absolute, err, absolute)
		} else if !info.IsDir() {
			return fmt.Errorf("--credentials-dir %s is not a directory", absolute)
		}
		plan.CredentialsDir = absolute
	}
	plan.CredentialsSecret = secret
	return nil
}

// describeCredentials reports whether the sidecar was given any way to
// authenticate to a model provider. Without one it enrolls, goes ready, and
// fails the first dispatch — so it is stated rather than discovered.
func describeCredentials(opts GitAgentDeployOptions) string {
	declared := len(opts.Env) + len(opts.EnvFromSecret)
	if strings.TrimSpace(opts.CredentialsSecret) != "" || strings.TrimSpace(opts.CredentialsDir) != "" {
		declared++
	}
	if declared == 0 {
		return "none declared — the agent cannot reach a model provider (--env / --env-from-secret / --credentials-secret)"
	}
	return fmt.Sprintf("%d source(s) declared", declared)
}

// backendPresets reads the sandbox presets the backend selects, so a preset
// granting a runtime socket is refused before anything is created.
func backendPresets(backendName string) ([]string, error) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}
	backend, ok := cfg.Sandbox.Backends[backendName]
	if !ok {
		return nil, nil
	}
	var presets []string
	switch declared := backend.Options["presets"].(type) {
	case []string:
		presets = declared
	case []any:
		for _, item := range declared {
			if name, ok := item.(string); ok {
				presets = append(presets, name)
			}
		}
	}
	return presets, nil
}

// replacementReusesEnrollment prevents replacement from rotating the identity
// stored on a managed deployment's retained state volume.
func replacementReusesEnrollment(opts GitAgentDeployOptions) (bool, error) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return false, err
	}
	if _, err := enrolledAgent(cfg, opts.Backend, opts.Name); err != nil {
		if opts.reuseEnrollment {
			return false, fmt.Errorf("agent %q no longer has an enrollment to reuse", opts.Name)
		}
		return false, nil
	}
	if !opts.Replace {
		return false, fmt.Errorf(
			"agent %q is already enrolled in backend %q; re-enrolling would repoint the supervisor at a new key and "+
				"leave any existing sidecar running with one that is no longer authorized. "+
				"Pass --replace to restart its managed workload with the same enrollment, or pick another name",
			opts.Name, opts.Backend)
	}
	if _, found := lookupDeployment(opts.Backend, opts.Name); !found {
		return false, fmt.Errorf(
			"agent %q is enrolled but has no Captain-managed deployment whose state can be retained; "+
				"replace cannot preserve its identity", opts.Name)
	}
	return true, nil
}

func printResolvedTarget(plan deploy.Plan, result GitAgentDeployResult, timeout time.Duration) {
	clicky.Printf("deploying git-agent %q\n", plan.Name)
	if plan.Target == deploy.TargetKubernetes {
		clicky.Printf("  cluster:      %s\n", kubeTargetDescription(result.Namespace))
	} else {
		clicky.Printf("  docker host:  %s\n", dockerHostDescription())
	}
	clicky.Printf("  image:        %s\n", plan.Image)
	clicky.Printf("  reaches mailbox at: %s (%s)\n", plan.Supervisor, result.SupervisorFrom)
	// Printed directly under the line whose credibility it qualifies, and
	// explicit that it is not the path the sidecar takes.
	if len(result.OffHostAddresses) > 0 {
		clicky.Printf("  off-loopback proof: %s answered (the sidecar dials the name above, not these)\n",
			strings.Join(result.OffHostAddresses, ", "))
	}
	clicky.Printf("  dispatched to at:   %s (%s)\n", plan.Advertise, result.AdvertiseFrom)
	// The DNS record is the one precondition this command cannot create and
	// cannot prove from here, so it is stated every time rather than discovered
	// when the first dispatch goes unanswered.
	if plan.HasExternalRoute() {
		clicky.Printf("  ingress:      %s (class %s, %s)\n",
			plan.ExternalRoute.Host, plan.ExternalRoute.ClassName, describeRouteCertificate(plan.ExternalRoute))
		clicky.Printf("  DNS required: %s must resolve to the %s ingress controller\n",
			plan.ExternalRoute.Host, plan.ExternalRoute.ClassName)
	}
	clicky.Printf("  security:     %s\n", result.Security)
	clicky.Printf("  credentials:  %s\n", result.Credentials)
	clicky.Printf("  timeout:      %s\n", timeout)
}

func kubeTargetDescription(namespace string) string {
	if namespace == "" {
		namespace = "<the kubeconfig context's namespace>"
	}
	return fmt.Sprintf("namespace %s", namespace)
}

func dockerHostDescription() string {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); host != "" {
		return host
	}
	return "the local docker daemon"
}

// deployMutations lists every change a deploy intends, in the order it makes
// them. It is the single source for both the CLI's --dry-run output and the web
// UI's preview, so the two cannot describe different deployments.
func deployMutations(plan deploy.Plan, opts GitAgentDeployOptions) []string {
	var mutations []string
	if opts.Replace {
		mutations = append(mutations, fmt.Sprintf(
			"remove the existing workload %s and retain its state volume %s",
			plan.WorkloadName(), plan.VolumeName()))
	}
	if opts.reuseEnrollment {
		mutations = append(mutations, fmt.Sprintf(
			"reuse the existing durable enrollment for agent %q from its retained state volume", plan.Name))
	} else {
		mutations = append(mutations,
			fmt.Sprintf("mint a durable captain token for agent %q", plan.Name),
			fmt.Sprintf("record the dispatch key under sandbox.backends.%s in %s", plan.Backend, configPathForDisplay()),
		)
	}
	switch plan.Target {
	case deploy.TargetDocker:
		joinPath := ""
		if !opts.reuseEnrollment {
			joinPath = joinTokenPath(plan)
			mutations = append(mutations, fmt.Sprintf("write the token to %s (0600)", joinPath))
		}
		mutations = append(mutations,
			"pull "+plan.Image,
			"run: docker "+strings.Join(deploy.DockerArgs(plan, joinPath), " "))
	case deploy.TargetKubernetes:
		mutations = append(mutations, kubernetesMutations(plan, opts)...)
	}
	return append(mutations,
		fmt.Sprintf("record the deployment under sandbox.backends.%s.deployments so undeploy knows where it went", plan.Backend))
}

// kubernetesMutations lists the cluster changes, and the one precondition this
// deploy pointedly does NOT create.
func kubernetesMutations(plan deploy.Plan, opts GitAgentDeployOptions) []string {
	var mutations []string
	if opts.CreateNamespace {
		// Listed separately because it is the one cluster-scoped change here,
		// and it outlives an undeploy.
		mutations = append(mutations,
			fmt.Sprintf("create %s if it does not exist", kubeTargetDescription(opts.Namespace)))
	}
	mutations = append(mutations, fmt.Sprintf("apply to %s as field manager %s",
		kubeTargetDescription(opts.Namespace), deploy.FieldManager))
	objects := []string{"PersistentVolumeClaim/" + plan.VolumeName(),
		"Service/" + plan.WorkloadName(),
		"Deployment/" + plan.WorkloadName(),
	}
	if !opts.reuseEnrollment {
		objects = append([]string{"Secret/" + plan.JoinSecretName()}, objects...)
	}
	if plan.HasExternalRoute() {
		objects = append(objects, "Ingress/"+plan.IngressName())
	}
	for _, object := range objects {
		mutations = append(mutations, "  "+object)
	}
	if plan.CredentialsSecret != "" {
		// Read, never written, by this deploy: the Secret is owned by the
		// credential publisher and shared with every other agent in the
		// namespace, so undeploy must not remove it either.
		mutations = append(mutations, fmt.Sprintf("mount existing Secret/%s read-only at %s (not created or deleted here)",
			plan.CredentialsSecret, deploy.CredentialsMountPath))
	}
	if !plan.HasExternalRoute() {
		return mutations
	}
	mutations = append(mutations, fmt.Sprintf("route https://%s%s to it, with the certificate in Secret/%s (%s)",
		plan.ExternalRoute.Host, gitagent.GitHTTPPrefix, plan.IngressTLSSecretName(),
		describeRouteCertificate(plan.ExternalRoute)))
	// deployMutations is documented as every change the deploy intends, and the
	// most consequential thing about this feature is a change it does not make.
	return append(mutations, fmt.Sprintf(
		"NOT create the DNS record: %s must already resolve to the %s ingress controller, or the certificate "+
			"never issues and the supervisor cannot reach the agent",
		plan.ExternalRoute.Host, plan.ExternalRoute.ClassName))
}

func printDeployDryRun(mutations []string) {
	for _, mutation := range mutations {
		clicky.Printf("[dry-run] would %s\n", mutation)
	}
	clicky.Printf("[dry-run] the token is never printed and never enters the workload's argv\n")
}

// joinTokenPath is the host-side token file for a docker deployment, kept
// beside the other key material so it inherits that directory's permissions.
func joinTokenPath(plan deploy.Plan) string {
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return filepath.Join(os.TempDir(), plan.WorkloadName(), "join")
	}
	return filepath.Join(keysDir, "deploy", plan.Name, "join")
}

// describeRouteCertificate names where the certificate for the route comes from.
func describeRouteCertificate(route deploy.ExternalRoute) string {
	if route.ClusterIssuer != "" {
		return "issuer " + route.ClusterIssuer
	}
	return "secret " + route.TLSSecret
}
