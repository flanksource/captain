package deploy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

const routedHost = "worker-01.agents.example.com"

// routedPlan is the externally-routed topology: a supervisor outside the
// cluster, reaching the sidecar through an ingress controller over https.
func routedPlan() deploy.Plan {
	plan := kubernetesPlan()
	plan.Transport = "https"
	plan.Advertise = "https://" + routedHost + "/git/repo.git"
	plan.ExternalRoute = deploy.ExternalRoute{
		Host:          routedHost,
		ClassName:     "nginx",
		ClusterIssuer: "letsencrypt-prod",
	}
	return plan
}

func routeAnnotations(plan deploy.Plan) map[string]any {
	GinkgoHelper()
	annotations, ok := dig(asJSON(plan.Ingress(testNamespace)), "metadata", "annotations").(map[string]any)
	Expect(ok).To(BeTrue())
	return annotations
}

var _ = Describe("Ingress", func() {
	// Each entry names the failure the setting prevents, because every one of
	// them fails late — on a large or slow task, never on a small fast one.
	DescribeTable("carries the settings this transport cannot work without",
		func(key, want string) {
			Expect(routeAnnotations(routedPlan())).To(HaveKeyWithValue(key, want))
		},
		Entry("re-encrypts to the pod's own TLS instead of crossing the cluster in clear text",
			"nginx.ingress.kubernetes.io/backend-protocol", "HTTPS"),
		Entry("streams the verdict instead of holding it until receive-pack exits",
			"nginx.ingress.kubernetes.io/proxy-buffering", "off"),
		Entry("starts receive-pack before the last byte of the packfile lands",
			"nginx.ingress.kubernetes.io/proxy-request-buffering", "off"),
		Entry("does not cap a packfile at nginx's 1m default",
			"nginx.ingress.kubernetes.io/proxy-body-size", "0"),
		Entry("outlasts a prompt hook rather than killing the push mid-verdict",
			"nginx.ingress.kubernetes.io/proxy-read-timeout", "3600"),
		Entry("outlasts a slow upload too",
			"nginx.ingress.kubernetes.io/proxy-send-timeout", "3600"),
		Entry("asks cert-manager for the certificate the supervisor will verify",
			"cert-manager.io/cluster-issuer", "letsencrypt-prod"),
	)

	It("routes only the git prefix and runtime identity endpoint without rewriting", func() {
		object := asJSON(routedPlan().Ingress(testNamespace))
		rules := dig(object, "spec", "rules").([]any)
		Expect(rules).To(HaveLen(1))

		rule := rules[0].(map[string]any)
		Expect(rule["host"]).To(Equal(routedHost))
		paths := dig(rule["http"].(map[string]any), "paths").([]any)
		Expect(paths).To(HaveLen(2))
		Expect(paths).To(ConsistOf(
			HaveKeyWithValue("path", "/git"),
			HaveKeyWithValue("path", "/api/v1/whoami"),
		))
		for _, raw := range paths {
			path := raw.(map[string]any)
			if path["path"] == "/git" {
				Expect(path["pathType"]).To(Equal("Prefix"))
			} else {
				Expect(path["pathType"]).To(Equal("Exact"))
			}
		}
		// HTTPSRepoURL bakes /git/ into every push URL, so stripping it here
		// would hand receive-pack a path it does not serve.
		for key := range routeAnnotations(routedPlan()) {
			Expect(key).NotTo(ContainSubstring("rewrite-target"))
		}
	})

	// The pod runs the same binary that serves captain's entire API. A rule for
	// "/" would publish that to the internet alongside the one endpoint the
	// supervisor needs.
	It("publishes no catch-all route", func() {
		object := asJSON(routedPlan().Ingress(testNamespace))
		Expect(object["spec"].(map[string]any)).NotTo(HaveKey("defaultBackend"))

		rule := dig(object, "spec", "rules").([]any)[0].(map[string]any)
		for _, p := range dig(rule["http"].(map[string]any), "paths").([]any) {
			Expect(p.(map[string]any)["path"]).NotTo(Equal("/"))
		}
	})

	It("terminates TLS for its own host into a per-agent secret", func() {
		tls := dig(asJSON(routedPlan().Ingress(testNamespace)), "spec", "tls").([]any)[0].(map[string]any)
		Expect(tls["hosts"]).To(ConsistOf(routedHost))
		Expect(tls["secretName"]).To(Equal("captain-git-agent-worker-01-tls"))
	})

	// An Ingress naming no class falls to whichever controller carries the
	// default annotation, and the wrong controller has none of the settings above.
	It("names the controller rather than relying on a default ingress class", func() {
		Expect(dig(asJSON(routedPlan().Ingress(testNamespace)), "spec", "ingressClassName")).To(Equal("nginx"))
	})

	It("backs onto the Service by port name, so the listen port can move", func() {
		rule := dig(asJSON(routedPlan().Ingress(testNamespace)), "spec", "rules").([]any)[0].(map[string]any)
		path := dig(rule["http"].(map[string]any), "paths").([]any)[0].(map[string]any)
		service := dig(path["backend"].(map[string]any), "service").(map[string]any)

		Expect(service["name"]).To(Equal("captain-git-agent-worker-01"))
		port := service["port"].(map[string]any)
		Expect(port["name"]).To(Equal("git"))
		Expect(port).NotTo(HaveKey("number"))
	})

	// Teardown deletes the name it derives. Removing an operator's shared
	// wildcard would take every other agent on that domain offline.
	It("uses an operator's own certificate without asking cert-manager", func() {
		plan := routedPlan()
		plan.ExternalRoute.ClusterIssuer = ""
		plan.ExternalRoute.TLSSecret = "wildcard-agents"

		tls := dig(asJSON(plan.Ingress(testNamespace)), "spec", "tls").([]any)[0].(map[string]any)
		Expect(tls["secretName"]).To(Equal("wildcard-agents"))
		Expect(routeAnnotations(plan)).NotTo(HaveKey("cert-manager.io/cluster-issuer"))
	})

	It("lets an operator override a default", func() {
		plan := routedPlan()
		plan.ExternalRoute.Annotations = map[string]string{
			"nginx.ingress.kubernetes.io/proxy-read-timeout":     "7200",
			"nginx.ingress.kubernetes.io/whitelist-source-range": "203.0.113.7/32",
		}
		annotations := routeAnnotations(plan)
		Expect(annotations).To(HaveKeyWithValue("nginx.ingress.kubernetes.io/proxy-read-timeout", "7200"))
		Expect(annotations).To(HaveKeyWithValue(
			"nginx.ingress.kubernetes.io/whitelist-source-range", "203.0.113.7/32"))
		// Overriding one must not drop the rest.
		Expect(annotations).To(HaveKeyWithValue("nginx.ingress.kubernetes.io/proxy-buffering", "off"))
	})

	It("pins Traefik's verified backend TLS to the route certificate", func() {
		plan := routedPlan()
		plan.ExternalRoute.ClassName = "traefik"

		service := asJSON(plan.Service(testNamespace))
		annotations := dig(service, "metadata", "annotations").(map[string]any)
		Expect(annotations).To(HaveKeyWithValue(
			"traefik.ingress.kubernetes.io/service.serversscheme", "https"))
		Expect(annotations).To(HaveKeyWithValue(
			"traefik.ingress.kubernetes.io/service.serverstransport",
			"agents-captain-git-agent-worker-01@kubernetescrd"))
		Expect(routeAnnotations(plan)).NotTo(HaveKey(
			"traefik.ingress.kubernetes.io/service.serversscheme"))
	})

	It("verifies Traefik's backend with the route hostname and issuing CA", func() {
		plan := routedPlan()
		plan.ExternalRoute.ClassName = "traefik"

		transport := asJSON(plan.TraefikServersTransport(testNamespace))
		Expect(transport).To(HaveKeyWithValue("apiVersion", "traefik.io/v1alpha1"))
		Expect(transport).To(HaveKeyWithValue("kind", "ServersTransport"))
		Expect(dig(transport, "metadata", "name")).To(Equal("captain-git-agent-worker-01"))
		Expect(dig(transport, "metadata", "namespace")).To(Equal(testNamespace))
		Expect(dig(transport, "spec", "serverName")).To(Equal(routedHost))
		Expect(dig(transport, "spec", "rootCAs")).To(ConsistOf(
			map[string]any{"secret": "captain-git-agent-worker-01-tls"}))
		Expect(dig(transport, "spec")).NotTo(HaveKey("insecureSkipVerify"))
	})

	It("labels the route so teardown can find it by selector", func() {
		labels := dig(asJSON(routedPlan().Ingress(testNamespace)), "metadata", "labels").(map[string]any)
		Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "captain"))
		Expect(labels).To(HaveKeyWithValue("app.kubernetes.io/instance", "worker-01"))
	})
})

// undeploy rebuilds a Plan from a name, a backend and a target and nothing else,
// so it cannot know whether a route was rendered. Deriving every route name from
// the workload name is what lets it delete one anyway.
var _ = Describe("external route naming", func() {
	It("derives the route names from the workload name alone", func() {
		bare := deploy.Plan{Name: "worker-01"}
		Expect(bare.HasExternalRoute()).To(BeFalse())
		Expect(bare.IngressName()).To(Equal("captain-git-agent-worker-01"))
		Expect(bare.IngressTLSSecretName()).To(Equal("captain-git-agent-worker-01-tls"))
	})

	It("reports a route only once a host is resolved", func() {
		Expect(routedPlan().HasExternalRoute()).To(BeTrue())
		Expect(kubernetesPlan().HasExternalRoute()).To(BeFalse())
	})
})
