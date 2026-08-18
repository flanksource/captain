package deploy

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsapply "k8s.io/client-go/applyconfigurations/apps/v1"
	coreapply "k8s.io/client-go/applyconfigurations/core/v1"
	metaapply "k8s.io/client-go/applyconfigurations/meta/v1"
)

// gitPortName is the transport port on the pod, the Service's targetPort, and
// the Ingress backend's port name.
//
// One name for both topologies: it is the git transport port whichever protocol
// rides it, and a name that branched would let the Service and the Ingress
// disagree about the same port. It was "git-ssh" while ssh was the only option.
const gitPortName = "git"

// FieldManager owns every field this package sets under server-side apply.
// Scoping ownership this way is what makes a re-deploy converge instead of
// failing on AlreadyExists, and leaves fields an operator added untouched.
const FieldManager = "captain-sandbox-git-agent"

// containerName is the single container in the sidecar pod.
const containerName = "git-agent"

// joinSecretKey is the key inside the join Secret.
const joinSecretKey = "join"

// credentialsVolumeName is the pod volume carrying the redacted agent logins.
const credentialsVolumeName = "credentials"

const (
	routeTLSVolumeName = "route-tls"
	routeTLSMountPath  = "/run/captain/tls"
)

// JoinSecret carries the single-use token to the pod as a mounted file.
//
// Immutable because a token is spent once: a Secret that could be edited in
// place would invite re-seeding a pod with a token the supervisor has burned,
// which fails identically on every restart.
func (p Plan) JoinSecret(namespace, token string) *coreapply.SecretApplyConfiguration {
	return coreapply.Secret(p.JoinSecretName(), namespace).
		WithLabels(p.Labels()).
		WithType(corev1.SecretTypeOpaque).
		WithImmutable(true).
		WithStringData(map[string]string{joinSecretKey: token})
}

// StateClaim is the volume holding everything that must outlive a restart.
//
// That is more than the keys: the agent's private key, the served repositories
// and in-flight worktrees all live under HOME, but so does ~/.captain.yaml,
// which records the supervisor's authorized dispatch key. Losing the file while
// keeping the keys leaves an agent whose host key still matches but which
// refuses every dispatch — a storage failure that reads as an auth failure.
func (p Plan) StateClaim(namespace, storageClass string) *coreapply.PersistentVolumeClaimApplyConfiguration {
	resources := coreapply.VolumeResourceRequirements().
		WithRequests(corev1.ResourceList{corev1.ResourceStorage: p.Sizing.Storage})
	spec := coreapply.PersistentVolumeClaimSpec().
		WithAccessModes(corev1.ReadWriteOnce).
		WithResources(resources)
	if storageClass != "" {
		spec = spec.WithStorageClassName(storageClass)
	}
	return coreapply.PersistentVolumeClaim(p.VolumeName(), namespace).
		WithLabels(p.Labels()).
		WithSpec(spec)
}

// Service gives the sidecar a stable address for the supervisor to dispatch to.
//
// It is not optional. The supervisor records the agent's URL once, at
// enrollment, so a pod IP would be wrong after the first reschedule — and the
// roster would still look healthy.
func (p Plan) Service(namespace string) *coreapply.ServiceApplyConfiguration {
	return coreapply.Service(p.WorkloadName(), namespace).
		WithLabels(p.Labels()).
		WithAnnotations(p.serviceAnnotations(namespace)).
		WithSpec(coreapply.ServiceSpec().
			WithType(corev1.ServiceTypeClusterIP).
			WithSelector(p.Labels()).
			WithPorts(coreapply.ServicePort().
				WithName(gitPortName).
				WithPort(int32(p.ListenPort)).
				WithTargetPort(intstr.FromString(gitPortName))))
}

// Deployment runs the sidecar.
//
// Recreate, not the default RollingUpdate: the state volume is ReadWriteOnce,
// so a rollout that starts the new pod before stopping the old one deadlocks on
// the claim. Recreate also gives the at-most-one semantics a single enrolled
// identity requires — two pods sharing one agent key would race the same
// mailbox.
func (p Plan) Deployment(namespace, pullPolicy, pullSecret string) *appsapply.DeploymentApplyConfiguration {
	return appsapply.Deployment(p.WorkloadName(), namespace).
		WithLabels(p.Labels()).
		WithSpec(appsapply.DeploymentSpec().
			WithReplicas(1).
			WithStrategy(appsapply.DeploymentStrategy().WithType("Recreate")).
			WithSelector(metaapply.LabelSelector().WithMatchLabels(p.Labels())).
			WithTemplate(p.podTemplate(pullPolicy, pullSecret)))
}

func (p Plan) podTemplate(pullPolicy, pullSecret string) *coreapply.PodTemplateSpecApplyConfiguration {
	spec := coreapply.PodSpec().
		// The pod runs agent-authored code (R5.2). A projected service-account
		// token would hand the model a cluster credential, and the sidecar has no
		// reason to talk to the API server.
		WithAutomountServiceAccountToken(false).
		// Keeps ambient *_SERVICE_HOST variables out of the untrusted process.
		WithEnableServiceLinks(false).
		WithTerminationGracePeriodSeconds(30).
		WithSecurityContext(p.podSecurityContext()).
		WithContainers(p.container(pullPolicy)).
		WithVolumes(
			coreapply.Volume().WithName("state").
				WithPersistentVolumeClaim(coreapply.PersistentVolumeClaimVolumeSource().
					WithClaimName(p.VolumeName())),
			coreapply.Volume().WithName("tmp").
				WithEmptyDir(coreapply.EmptyDirVolumeSource().WithSizeLimit(p.Sizing.TmpSize)),
		)
	if p.JoinPath != "" {
		spec = spec.WithVolumes(coreapply.Volume().WithName("join").
			WithSecret(coreapply.SecretVolumeSource().
				WithSecretName(p.JoinSecretName()).
				WithDefaultMode(0o400).
				WithOptional(true)))
	}
	if p.HasExternalRoute() {
		spec = spec.WithVolumes(coreapply.Volume().WithName(routeTLSVolumeName).
			WithSecret(coreapply.SecretVolumeSource().
				WithSecretName(p.IngressTLSSecretName()).
				WithDefaultMode(0o400)))
	}
	if p.CredentialsSecret != "" {
		spec = spec.WithVolumes(coreapply.Volume().WithName(credentialsVolumeName).
			WithSecret(coreapply.SecretVolumeSource().
				WithSecretName(p.CredentialsSecret).
				WithDefaultMode(0o400).
				// Optional so the pod still starts before the supervisor's first
				// publish, rather than hanging in ContainerCreating.
				WithOptional(true)))
	}
	if pullSecret != "" {
		spec = spec.WithImagePullSecrets(coreapply.LocalObjectReference().WithName(pullSecret))
	}
	return coreapply.PodTemplateSpec().WithLabels(p.Labels()).WithSpec(spec)
}

// podSecurityContext runs the workload as the image's unprivileged user.
//
// fsGroup is load-bearing rather than decorative: a freshly provisioned volume
// is root-owned, and the first thing the sidecar does is create its key
// directory at mode 0700. Without it that fails with EACCES on first start.
func (p Plan) podSecurityContext() *coreapply.PodSecurityContextApplyConfiguration {
	return coreapply.PodSecurityContext().
		WithRunAsNonRoot(true).
		WithRunAsUser(int64(p.Security.RunAsUser)).
		WithRunAsGroup(int64(p.Security.RunAsGroup)).
		WithFSGroup(int64(p.Security.RunAsGroup)).
		WithFSGroupChangePolicy(corev1.FSGroupChangeOnRootMismatch).
		WithSeccompProfile(coreapply.SeccompProfile().WithType(corev1.SeccompProfileTypeRuntimeDefault))
}

func (p Plan) container(pullPolicy string) *coreapply.ContainerApplyConfiguration {
	container := coreapply.Container().
		WithName(containerName).
		WithImage(p.Image).
		WithImagePullPolicy(corev1.PullPolicy(pullPolicy)).
		// Override the image entrypoint for the same reason docker does: it ends
		// `USER root` and calls gosu, which needs the CAP_SETUID/CAP_SETGID that
		// dropping all capabilities removes. Running the binary directly under
		// runAsUser means the process is never root.
		WithCommand("captain").
		WithArgs(p.ServeArgs()...).
		WithEnv(
			coreapply.EnvVar().WithName("HOME").WithValue(p.Home),
			coreapply.EnvVar().WithName("TMPDIR").WithValue(p.Home+"/.cache/tmp"),
		).
		WithPorts(coreapply.ContainerPort().WithName(gitPortName).WithContainerPort(int32(p.ListenPort))).
		WithResources(p.resources()).
		WithSecurityContext(p.containerSecurityContext()).
		WithVolumeMounts(
			coreapply.VolumeMount().WithName("state").WithMountPath(p.Home),
			coreapply.VolumeMount().WithName("tmp").WithMountPath("/tmp"),
		).
		// The receive endpoint answering TCP is the honest readiness signal: the
		// join exchange runs before ListenAndServe, so an open port means
		// enrollment already succeeded.
		WithReadinessProbe(tcpProbe(p.ListenPort, 5, 3)).
		// Deliberately lenient. A hook set can hold the process for minutes, and a
		// tight liveness probe would kill a blocked push mid-verification.
		WithLivenessProbe(tcpProbe(p.ListenPort, 30, 6))
	if p.JoinPath != "" {
		container = container.WithVolumeMounts(coreapply.VolumeMount().
			WithName("join").WithMountPath(joinMountDir(p.JoinPath)).WithReadOnly(true))
	}

	if p.CredentialsSecret != "" {
		// A directory mount, deliberately not a subPath one. Kubelet never
		// updates a subPath volume after the pod starts, so a subPath mount
		// would pin the workload to the first credential it ever saw and defeat
		// the republish loop entirely.
		container = container.WithVolumeMounts(coreapply.VolumeMount().
			WithName(credentialsVolumeName).
			WithMountPath(CredentialsMountPath).
			WithReadOnly(true))
	}
	if p.HasExternalRoute() {
		container = container.WithVolumeMounts(coreapply.VolumeMount().
			WithName(routeTLSVolumeName).
			WithMountPath(routeTLSMountPath).
			WithReadOnly(true))
	}
	for _, secret := range p.EnvFromSecrets {
		container = container.WithEnvFrom(coreapply.EnvFromSource().
			WithSecretRef(coreapply.SecretEnvSource().WithName(secret)))
	}
	return container
}

func (p Plan) containerSecurityContext() *coreapply.SecurityContextApplyConfiguration {
	capabilities := coreapply.Capabilities().WithDrop(corev1.Capability("ALL"))
	for _, capability := range p.Security.CapAdd {
		capabilities = capabilities.WithAdd(corev1.Capability(capability))
	}
	return coreapply.SecurityContext().
		WithPrivileged(false).
		WithAllowPrivilegeEscalation(false).
		WithReadOnlyRootFilesystem(p.Security.ReadOnlyRoot).
		WithCapabilities(capabilities)
}

// resources sets both halves of memory but only the CPU request.
//
// A CPU limit throttles rather than fails: the sidecar's work is compiling and
// testing, so a ceiling turns a slow build into a timed-out one without ever
// reporting why.
func (p Plan) resources() *coreapply.ResourceRequirementsApplyConfiguration {
	return coreapply.ResourceRequirements().
		WithRequests(corev1.ResourceList{
			corev1.ResourceCPU:              p.Sizing.CPURequest,
			corev1.ResourceMemory:           p.Sizing.MemoryRequest,
			corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
		}).
		WithLimits(corev1.ResourceList{
			corev1.ResourceMemory:           p.Sizing.MemoryLimit,
			corev1.ResourceEphemeralStorage: p.Sizing.TmpSize,
		})
}

func tcpProbe(port int, periodSeconds, failureThreshold int32) *coreapply.ProbeApplyConfiguration {
	return coreapply.Probe().
		WithTCPSocket(coreapply.TCPSocketAction().WithPort(intstr.FromInt32(int32(port)))).
		WithInitialDelaySeconds(5).
		WithPeriodSeconds(periodSeconds).
		WithFailureThreshold(failureThreshold)
}

// joinMountDir is the directory the token file sits in; Kubernetes mounts a
// Secret as a directory, with each key a file inside it.
func joinMountDir(joinPath string) string {
	if dir := joinPath[:len(joinPath)-len(joinSecretKey)]; len(dir) > 1 {
		return trimTrailingSlash(dir)
	}
	return joinPath
}

func trimTrailingSlash(path string) string {
	for len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	return path
}

// ValidateJoinPath ensures the mount path and the Secret key agree, so the file
// actually lands where --join-file looks for it.
func (p Plan) ValidateJoinPath() error {
	if p.JoinPath == "" {
		return nil
	}
	if joinMountDir(p.JoinPath)+"/"+joinSecretKey != p.JoinPath {
		return fmt.Errorf("join path %q must end in /%s so the mounted Secret key lands there", p.JoinPath, joinSecretKey)
	}
	return nil
}
