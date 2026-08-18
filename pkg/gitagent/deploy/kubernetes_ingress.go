// The external route: how a supervisor outside the cluster reaches a sidecar.
//
// Almost everything below exists because a git push over smart-HTTP violates
// every default a reverse proxy has. The request body is unbounded, the response
// streams a verdict the client must see WHILE the push is still open, and the
// whole exchange can take minutes because a prompt hook runs inside it. Each
// default left in place fails late rather than loudly: a small, fast task
// succeeds and a real one hangs, 413s, or loses its rejection.
package deploy

import (
	"maps"
	"strings"

	netv1 "k8s.io/api/networking/v1"
	netapply "k8s.io/client-go/applyconfigurations/networking/v1"

	"github.com/flanksource/captain/pkg/gitagent"
)

// routePathPrefix is the transport subtree published by the Ingress.
//
// Trimmed of its trailing slash because Prefix matching is by path element, so
// "/git" and "/git/" select the same set. Taken from the transport's own
// constant rather than restated, because the client hard-codes it in every push
// URL it builds and a second copy would drift.
var routePathPrefix = strings.TrimSuffix(gitagent.GitHTTPPrefix, "/")

const traefikServiceAnnotationPrefix = "traefik.ingress.kubernetes.io/service."

// Ingress fronts the sidecar for a supervisor outside the cluster.
//
// Only the git transport and the authenticated runtime identity endpoint are
// routed, both unrewritten. A rule for "/" would publish every future endpoint
// the agent binary grows; and stripping the git prefix would hand receive-pack a
// path it does not serve, because HTTPSRepoURL bakes it into every push URL.
func (p Plan) Ingress(namespace string) *netapply.IngressApplyConfiguration {
	backend := netapply.IngressBackend().WithService(netapply.IngressServiceBackend().
		WithName(p.WorkloadName()).
		// By name, not number, so the listen port can move without the route
		// silently pointing at nothing.
		WithPort(netapply.ServiceBackendPort().WithName(gitPortName)))

	paths := []*netapply.HTTPIngressPathApplyConfiguration{
		netapply.HTTPIngressPath().
			WithPath(routePathPrefix).
			WithPathType(netv1.PathTypePrefix).
			WithBackend(backend),
		netapply.HTTPIngressPath().
			WithPath(gitagent.AgentWhoamiPath).
			WithPathType(netv1.PathTypeExact).
			WithBackend(backend),
	}
	spec := netapply.IngressSpec().
		WithIngressClassName(p.ExternalRoute.ClassName).
		WithTLS(netapply.IngressTLS().
			WithHosts(p.ExternalRoute.Host).
			WithSecretName(p.IngressTLSSecretName())).
		WithRules(netapply.IngressRule().
			WithHost(p.ExternalRoute.Host).
			WithHTTP(netapply.HTTPIngressRuleValue().WithPaths(paths...)))

	return netapply.Ingress(p.IngressName(), namespace).
		WithLabels(p.Labels()).
		WithAnnotations(p.routeAnnotations()).
		WithSpec(spec)
}

// nginxRouteAnnotations are the ingress-nginx settings this transport cannot
// work without. Each one names the failure it prevents.
func nginxRouteAnnotations() map[string]string {
	return map[string]string{
		// The pod terminates its own TLS with a captain-generated certificate, so
		// the hop from the controller is re-encrypted rather than crossing the
		// cluster network in clear text with a bearer token in the header.
		// ingress-nginx does not verify an upstream certificate by default, which
		// is what lets a self-signed pod certificate work; the trust that matters
		// is the token and the certificate the supervisor validates at the edge.
		"nginx.ingress.kubernetes.io/backend-protocol": "HTTPS",

		// httpserver.go's flushWriter pushes each write through so a hook
		// rejection streams during the push. nginx buffers responses by default,
		// which holds the verdict until receive-pack exits: the operator sees a
		// push sit silent for minutes and then fail all at once, with no way to
		// tell a slow hook from a hung one.
		"nginx.ingress.kubernetes.io/proxy-buffering": "off",

		// The request half. With buffering on, nginx spools the entire packfile to
		// disk before opening the upstream connection, so receive-pack cannot
		// begin until the last byte lands — and the controller acquires a
		// disk-space failure mode nothing reports.
		"nginx.ingress.kubernetes.io/proxy-request-buffering": "off",

		// nginx caps a request body at 1m. A packfile routinely exceeds that, and
		// git reports the resulting 413 as "the remote end hung up unexpectedly",
		// which names neither the proxy nor the limit.
		"nginx.ingress.kubernetes.io/proxy-body-size": "0",

		// serve.go sets only ReadHeaderTimeout on purpose: a prompt hook can take
		// minutes. nginx's 60s read timeout would kill the push mid-verdict and
		// report a broken connection to the agent.
		"nginx.ingress.kubernetes.io/proxy-read-timeout": "3600",
		"nginx.ingress.kubernetes.io/proxy-send-timeout": "3600",
	}
}

// routeAnnotations layers the operator's annotations over the defaults.
func (p Plan) routeAnnotations() map[string]string {
	annotations := nginxRouteAnnotations()
	if p.ExternalRoute.ClusterIssuer != "" {
		annotations["cert-manager.io/cluster-issuer"] = p.ExternalRoute.ClusterIssuer
	}
	// Merged last so --ingress-annotation wins: raising a timeout for a slow hook
	// set has to be possible, and a controller that is not ingress-nginx needs
	// its own equivalents beside the inert nginx ones.
	maps.Copy(annotations, p.ExternalRoute.Annotations)
	for key := range annotations {
		if strings.HasPrefix(key, traefikServiceAnnotationPrefix) {
			delete(annotations, key)
		}
	}
	return annotations
}

func (p Plan) serviceAnnotations(namespace string) map[string]string {
	annotations := map[string]string{}
	if p.ExternalRoute.ClassName == "traefik" {
		annotations["traefik.ingress.kubernetes.io/service.serversscheme"] = "https"
		annotations["traefik.ingress.kubernetes.io/service.serverstransport"] =
			namespace + "-" + p.WorkloadName() + "@kubernetescrd"
	}
	for key, value := range p.ExternalRoute.Annotations {
		if strings.HasPrefix(key, traefikServiceAnnotationPrefix) {
			annotations[key] = value
		}
	}
	return annotations
}
