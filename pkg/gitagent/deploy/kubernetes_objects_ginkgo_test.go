package deploy_test

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

const testNamespace = "agents"

func kubernetesPlan() deploy.Plan {
	plan := dockerPlan()
	plan.Target = deploy.TargetKubernetes
	plan.HostPort = 0
	plan.Advertise = "ssh://captain@captain-git-agent-worker-01.agents.svc.cluster.local:7422/repo.git"
	plan.Supervisor = "ssh://captain@mailbox.internal:7422"
	return plan
}

// asJSON marshals an apply configuration the way the API server will see it.
func asJSON(object any) map[string]any {
	GinkgoHelper()
	raw, err := json.Marshal(object)
	Expect(err).NotTo(HaveOccurred())
	var decoded map[string]any
	Expect(json.Unmarshal(raw, &decoded)).To(Succeed())
	return decoded
}

// dig walks a nested JSON object, failing the spec if a step is missing.
func dig(object map[string]any, path ...string) any {
	GinkgoHelper()
	var current any = object
	for _, step := range path {
		asMap, ok := current.(map[string]any)
		Expect(ok).To(BeTrue(), "expected an object at %q in %v", step, path)
		current, ok = asMap[step]
		Expect(ok).To(BeTrue(), "missing %q in %v", step, path)
	}
	return current
}

var _ = Describe("Kubernetes objects", func() {
	plan := kubernetesPlan()

	Describe("Deployment", func() {
		var object map[string]any

		BeforeEach(func() {
			object = asJSON(plan.Deployment(testNamespace, "IfNotPresent", ""))
		})

		// A ReadWriteOnce claim cannot be held by two pods, so the default
		// RollingUpdate would deadlock the rollout on its own volume.
		It("recreates rather than rolling, and runs exactly one replica", func() {
			Expect(dig(object, "spec", "strategy", "type")).To(Equal("Recreate"))
			Expect(dig(object, "spec", "replicas")).To(BeEquivalentTo(1))
		})

		// The pod runs agent-authored code; a projected token is a cluster
		// credential handed to the model.
		It("mounts no service-account token and no service links", func() {
			Expect(dig(object, "spec", "template", "spec", "automountServiceAccountToken")).To(BeFalse())
			Expect(dig(object, "spec", "template", "spec", "enableServiceLinks")).To(BeFalse())
		})

		It("runs as the image's unprivileged user with a seccomp profile", func() {
			security := dig(object, "spec", "template", "spec", "securityContext").(map[string]any)
			Expect(security["runAsNonRoot"]).To(BeTrue())
			Expect(security["runAsUser"]).To(BeEquivalentTo(501))
			Expect(security["runAsGroup"]).To(BeEquivalentTo(20))
			// Without fsGroup a freshly provisioned volume is root-owned and the
			// first key write fails EACCES.
			Expect(security["fsGroup"]).To(BeEquivalentTo(20))
			Expect(dig(security, "seccompProfile", "type")).To(Equal("RuntimeDefault"))
		})

		It("drops every capability and forbids escalation", func() {
			security := dig(object, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["securityContext"].(map[string]any)
			Expect(security["privileged"]).To(BeFalse())
			Expect(security["allowPrivilegeEscalation"]).To(BeFalse())
			Expect(security["readOnlyRootFilesystem"]).To(BeTrue())
			Expect(dig(security, "capabilities", "drop")).To(ConsistOf("ALL"))
		})

		// gosu needs CAP_SETUID/CAP_SETGID, which dropping ALL removes.
		It("overrides the image entrypoint so it never starts as root", func() {
			container := dig(object, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
			Expect(container["command"]).To(ConsistOf("captain"))
			Expect(container["args"]).To(HaveLen(len(plan.ServeArgs())))
		})

		// A CPU ceiling throttles a build into a timeout without reporting why.
		It("limits memory but not CPU", func() {
			resources := dig(object, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["resources"].(map[string]any)
			Expect(resources["limits"]).To(HaveKey("memory"))
			Expect(resources["limits"]).NotTo(HaveKey("cpu"))
			Expect(resources["requests"]).To(HaveKey("cpu"))
		})

		It("mounts state at HOME so the config file survives with the keys", func() {
			mounts := dig(object, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)["volumeMounts"].([]any)
			paths := map[string]string{}
			for _, mount := range mounts {
				entry := mount.(map[string]any)
				paths[entry["name"].(string)] = entry["mountPath"].(string)
			}
			// ~/.captain.yaml is a sibling of ~/.captain/, and it holds the
			// authorized dispatch key. Mounting only the keys dir would keep the
			// keys and lose the authorization.
			Expect(paths["state"]).To(Equal("/home/claude"))
			Expect(paths["join"]).To(Equal("/run/captain"))
			Expect(paths["tmp"]).To(Equal("/tmp"))
		})

		// Deleted once enrollment lands; the pod must still restart afterwards.
		It("treats the join secret as optional and read-only", func() {
			for _, volume := range dig(object, "spec", "template", "spec", "volumes").([]any) {
				entry := volume.(map[string]any)
				if entry["name"] != "join" {
					continue
				}
				secret := entry["secret"].(map[string]any)
				Expect(secret["optional"]).To(BeTrue())
				Expect(secret["defaultMode"]).To(BeEquivalentTo(0o400))
				return
			}
			Fail("no join volume")
		})

		It("adds an image pull secret only when one is given", func() {
			Expect(dig(object, "spec", "template", "spec")).NotTo(HaveKey("imagePullSecrets"))

			withSecret := asJSON(plan.Deployment(testNamespace, "Always", "registry-creds"))
			Expect(dig(withSecret, "spec", "template", "spec", "imagePullSecrets")).
				To(ConsistOf(map[string]any{"name": "registry-creds"}))
		})

		It("mounts the route certificate and serves it instead of a pod-IP certificate", func() {
			deployment := asJSON(routedPlan().Deployment(testNamespace, "IfNotPresent", ""))
			podSpec := dig(deployment, "spec", "template", "spec").(map[string]any)
			Expect(podSpec["volumes"]).To(ContainElement(map[string]any{
				"name": "route-tls",
				"secret": map[string]any{
					"secretName":  "captain-git-agent-worker-01-tls",
					"defaultMode": float64(0o400),
				},
			}))

			container := podSpec["containers"].([]any)[0].(map[string]any)
			Expect(container["volumeMounts"]).To(ContainElement(map[string]any{
				"name": "route-tls", "mountPath": "/run/captain/tls", "readOnly": true,
			}))
			Expect(container["args"]).To(ContainElements(
				"--tls-cert", "/run/captain/tls/tls.crt",
				"--tls-key", "/run/captain/tls/tls.key",
			))
		})
	})

	Describe("Service", func() {
		// The supervisor records the agent URL once at enrollment, so a pod IP
		// would be stale after the first reschedule.
		It("fronts the pod on a stable cluster name", func() {
			object := asJSON(plan.Service(testNamespace))
			Expect(dig(object, "spec", "type")).To(Equal("ClusterIP"))
			Expect(dig(object, "spec", "selector")).To(Equal(toAnyMap(plan.Labels())))
			port := dig(object, "spec", "ports").([]any)[0].(map[string]any)
			Expect(port["port"]).To(BeEquivalentTo(7422))
			// By name, so the Ingress backend and the container agree on one
			// port even if the listen port moves. Not "git-ssh": the same port
			// carries https in the externally-routed topology.
			Expect(port["targetPort"]).To(Equal("git"))
		})

		// The Service's targetPort dereferences the container's port name, and the
		// Ingress backend dereferences the Service's. A name that differed between
		// them would route to nothing.
		It("names the port identically on the container in both topologies", func() {
			for _, p := range []deploy.Plan{plan, routedPlan()} {
				container := dig(asJSON(p.Deployment(testNamespace, "IfNotPresent", "")),
					"spec", "template", "spec", "containers").([]any)[0].(map[string]any)
				ports := container["ports"].([]any)[0].(map[string]any)
				Expect(ports["name"]).To(Equal("git"))
			}
		})
	})

	Describe("StateClaim", func() {
		It("requests the configured size, defaulting the storage class to the cluster's", func() {
			object := asJSON(plan.StateClaim(testNamespace, ""))
			Expect(dig(object, "spec", "accessModes")).To(ConsistOf("ReadWriteOnce"))
			Expect(dig(object, "spec", "resources", "requests", "storage")).To(Equal("20Gi"))
			Expect(dig(object, "spec")).NotTo(HaveKey("storageClassName"))

			withClass := asJSON(plan.StateClaim(testNamespace, "fast"))
			Expect(dig(withClass, "spec", "storageClassName")).To(Equal("fast"))
		})
	})

	Describe("JoinSecret", func() {
		It("is immutable, because a spent token cannot be reused", func() {
			object := asJSON(plan.JoinSecret(testNamespace, "s3cret-join-token"))
			Expect(object["immutable"]).To(BeTrue())
			Expect(dig(object, "stringData", "join")).To(Equal("s3cret-join-token"))
		})
	})

	// R8.2. In a pod spec the token would be readable via `kubectl get -o yaml`
	// and from etcd; in env it would additionally be readable from
	// /proc/1/environ by the coding agent this pod runs.
	It("keeps the token out of every object except the Secret", func() {
		const token = "s3cret-join-token"
		for name, object := range map[string]any{
			"Deployment": plan.Deployment(testNamespace, "IfNotPresent", ""),
			"Service":    plan.Service(testNamespace),
			"PVC":        plan.StateClaim(testNamespace, ""),
		} {
			raw, err := json.Marshal(object)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).NotTo(ContainSubstring(token), "%s carries the join token", name)
			Expect(string(raw)).NotTo(ContainSubstring("--join "), "%s passes the token in argv", name)
		}

		// And no env var carries it either.
		deployment := asJSON(plan.Deployment(testNamespace, "IfNotPresent", ""))
		container := dig(deployment, "spec", "template", "spec", "containers").([]any)[0].(map[string]any)
		for _, env := range container["env"].([]any) {
			Expect(env.(map[string]any)["value"]).NotTo(ContainSubstring(token))
		}
		Expect(container).NotTo(HaveKey("envFrom"))
	})

	It("exposes declared secrets through envFrom", func() {
		withEnv := plan
		withEnv.EnvFromSecrets = []string{"model-credentials"}
		container := dig(asJSON(withEnv.Deployment(testNamespace, "IfNotPresent", "")),
			"spec", "template", "spec", "containers").([]any)[0].(map[string]any)
		Expect(container["envFrom"]).To(ConsistOf(
			map[string]any{"secretRef": map[string]any{"name": "model-credentials"}}))
	})

	Describe("ValidateJoinPath", func() {
		It("accepts a path whose basename is the secret key", func() {
			Expect(plan.ValidateJoinPath()).To(Succeed())
			Expect(strings.HasSuffix(plan.JoinPath, "/join")).To(BeTrue())
		})

		It("refuses a path the mounted key would never land on", func() {
			wrong := plan
			wrong.JoinPath = "/run/captain/token"
			Expect(wrong.ValidateJoinPath()).To(MatchError(ContainSubstring("must end in /join")))
		})
	})
})

func toAnyMap(in map[string]string) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}
