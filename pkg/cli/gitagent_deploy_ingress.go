// Resolving the external route for `captain sandbox git-agent deploy`.
//
// A Kubernetes sidecar's advertise address is the one thing in this command that
// cannot be detected: a supervisor outside the cluster cannot dial a ClusterIP,
// and the name that would work does not exist until someone creates a DNS record
// captain has no way to see. So the flags either name it or the deploy refuses,
// and the one change this feature does NOT make is stated out loud.
package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// dnsLabel is what a single DNS name component may be.
//
// Agent names are validated as ^[a-z0-9-]{1,64}$, which is looser: it permits a
// leading or trailing hyphen and one more character than DNS allows. The API
// server accepts such a host in spec.rules[].host — it validates a laxer
// wildcard-subdomain shape — and the name then simply never resolves.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// applyExternalRoute puts the external route on the plan, or leaves it empty for
// the in-cluster topology.
//
// Modelled on applyCredentialMount: a flag belonging to the other target is
// refused rather than ignored, because an ignored --domain looks configured and
// leaves an operator waiting on a hostname nothing ever created.
func applyExternalRoute(plan *deploy.Plan, opts GitAgentDeployOptions, target deploy.Target) error {
	domain := strings.TrimSpace(opts.Domain)
	issuer := strings.TrimSpace(opts.IngressIssuer)
	secret := strings.TrimSpace(opts.IngressTLSSecret)
	declared := domain != "" || issuer != "" || secret != "" || len(opts.IngressAnnotation) > 0

	if declared && target != deploy.TargetKubernetes {
		return fmt.Errorf("--domain needs --target kubernetes; a docker sidecar is reached on its published loopback port")
	}
	if !declared {
		return nil
	}
	if domain == "" {
		return fmt.Errorf(
			"--ingress-issuer has no effect without --domain; without a domain no Ingress is created and the " +
				"agent is only reachable inside the cluster")
	}
	switch {
	case issuer != "" && secret != "":
		return fmt.Errorf("--ingress-issuer and --ingress-tls-secret are mutually exclusive")
	case issuer == "" && secret == "":
		return fmt.Errorf(
			"--domain %s needs a certificate for the agent's host: pass --ingress-issuer <cert-manager "+
				"ClusterIssuer>, or --ingress-tls-secret naming one you already have. Without either, the "+
				"controller answers for that host with its own default certificate and the supervisor's push "+
				"fails verification", domain)
	}
	host, err := resolveExternalHost(plan.Name, domain)
	if err != nil {
		return err
	}
	annotations, err := parseIngressAnnotations(opts.IngressAnnotation)
	if err != nil {
		return err
	}
	class := strings.TrimSpace(opts.IngressClass)
	if err := refuseUntranslatedController(class, annotations); err != nil {
		return err
	}
	if err := refuseConflictingAdvertise(opts.Advertise, host); err != nil {
		return err
	}
	plan.ExternalRoute = deploy.ExternalRoute{
		Host: host, ClassName: class, ClusterIssuer: issuer, TLSSecret: secret, Annotations: annotations,
	}
	// The workload has to serve the protocol the route re-encrypts to; serving
	// the other one accepts the connection and fails the handshake.
	plan.Transport = string(transportHTTPS)
	return nil
}

// resolveExternalHost derives the name the supervisor dials, and proves it is a
// name that can resolve.
func resolveExternalHost(name, domain string) (string, error) {
	host := name + "." + strings.Trim(strings.TrimSpace(domain), ".")
	for _, label := range strings.Split(host, ".") {
		if !dnsLabel.MatchString(label) {
			return "", fmt.Errorf(
				"the agent's host would be %q, which is not a valid DNS name (%q is not a label); agent names "+
					"are validated more loosely than DNS allows — rename the agent or pass a different --domain",
				host, label)
		}
	}
	if len(host) > 253 {
		return "", fmt.Errorf("the agent's host would be %q, which is longer than a DNS name may be", host)
	}
	return host, nil
}

// parseIngressAnnotations turns key=value entries into the map the route merges.
func parseIngressAnnotations(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	annotations := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, found := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, fmt.Errorf("--ingress-annotation %q must be key=value", entry)
		}
		annotations[key] = value
	}
	return annotations, nil
}

// refuseUntranslatedController stops a deploy onto a controller whose vocabulary
// captain does not speak.
//
// It is an acknowledgement gate rather than a validation: any annotation
// satisfies it, because captain cannot check another controller's spelling. That
// is the strongest honest thing available, and the message says what has to be
// covered.
func refuseUntranslatedController(class string, annotations map[string]string) error {
	if class == "" {
		return fmt.Errorf("--ingress-class names the controller serving --domain; an Ingress with no class " +
			"falls to whichever IngressClass is marked default, which may not be the one you mean")
	}
	if class == "nginx" || class == "traefik" || len(annotations) > 0 {
		return nil
	}
	return fmt.Errorf(
		"--ingress-class %q is not ingress-nginx, so the buffering, body-size and timeout settings this "+
			"transport depends on cannot be written as nginx.ingress.kubernetes.io annotations — they would be "+
			"applied and silently ignored. A push streams its verdict while it runs and can take minutes, so a "+
			"controller that buffers responses, caps the request body, or times out at 60s fails only on the "+
			"large or slow tasks. State the equivalents for %s with --ingress-annotation key=value (at minimum: "+
			"response buffering off, request buffering off, request body uncapped, read timeout of at least an "+
			"hour), or use --ingress-class nginx", class, class)
}

// refuseConflictingAdvertise stops the two names that must agree from differing.
func refuseConflictingAdvertise(advertise, host string) error {
	given := strings.TrimSpace(advertise)
	if given == "" {
		return nil
	}
	parsed, err := url.Parse(given)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("--advertise %q must be https://host[:port] when used with --domain", advertise)
	}
	if parsed.Hostname() == host {
		return nil
	}
	return fmt.Errorf(
		"--advertise %s names host %q but --domain publishes this agent at %q; the Ingress routes only the "+
			"host in its rule, so a dispatch to the other name gets a 404 from the controller. Drop one of them",
		advertise, parsed.Hostname(), host)
}

// refuseUnroutableExternalRoute checks, before the mint, the two cluster facts
// that would otherwise produce an agent that enrolls, reports ready, and is
// never dispatchable.
//
// Both skip silently when the cluster will not answer, the same rule
// CheckPermissions applies: absence of the check is not a failed check.
func refuseUnroutableExternalRoute(ctx context.Context, client kubernetes.Interface, route deploy.ExternalRoute) error {
	if err := refuseMissingIngressClass(ctx, client, route.ClassName); err != nil {
		return err
	}
	if err := refuseMissingCertManager(ctx, client, route.ClusterIssuer); err != nil {
		return err
	}
	return refuseUnresolvableHost(ctx, route.Host)
}

// refuseMissingIngressClass is the highest-value check here. An Ingress naming a
// class no controller implements is accepted by the API server and then simply
// never routed: the deploy succeeds, the pod goes ready, the agent enrolls, and
// the first dispatch — hours later — gets no answer at all.
func refuseMissingIngressClass(ctx context.Context, client kubernetes.Interface, class string) error {
	list, err := client.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil //nolint:nilerr // absence of the check is not a failed check
	}
	available := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Name == class {
			return nil
		}
		available = append(available, item.Name)
	}
	return fmt.Errorf(
		"no IngressClass named %q exists in this cluster, so the Ingress would be created and never routed — "+
			"the agent would enroll, report ready, and answer no dispatch. Available: %s",
		class, describeAvailable(available))
}

// refuseMissingCertManager catches an issuer annotation that is inert, which
// leaves the controller answering for the host with its own default certificate
// and the supervisor's push failing verification against a name it does not hold.
func refuseMissingCertManager(ctx context.Context, client kubernetes.Interface, issuer string) error {
	if issuer == "" {
		return nil
	}
	if _, err := client.Discovery().ServerResourcesForGroupVersion("cert-manager.io/v1"); err != nil {
		if apiGroupMissing(err) {
			return fmt.Errorf(
				"--ingress-issuer %s names a cert-manager ClusterIssuer, but cert-manager is not installed in "+
					"this cluster, so the annotation would be ignored and no certificate issued; install "+
					"cert-manager, or pass --ingress-tls-secret naming a certificate you already have", issuer)
		}
		return nil //nolint:nilerr // absence of the check is not a failed check
	}
	return nil
}

// refuseUnresolvableHost catches the precondition captain cannot create.
//
// With an HTTP-01 challenge the host must already resolve to the controller
// before the Ingress lands, or the challenge fails and the certificate never
// issues. NXDOMAIN is a refusal; a resolver that could not be reached is not,
// because "the name does not exist" and "I could not ask" call for different
// next steps.
func refuseUnresolvableHost(ctx context.Context, host string) error {
	if _, err := net.DefaultResolver.LookupHost(ctx, host); err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return fmt.Errorf(
				"%s does not resolve, so the supervisor could not reach this agent and a cert-manager HTTP-01 "+
					"challenge for it would fail; create a DNS record pointing it at the ingress controller "+
					"first, then re-run", host)
		}
	}
	return nil
}

// refuseDuplicateRouteHost stops two agents in one namespace claiming a host.
//
// ingress-nginx resolves a collision by oldest creationTimestamp with only a log
// line, so the newer agent's dispatches would go to the older pod. Only the
// target namespace is checked: another namespace or another cluster serving the
// same domain is genuinely outside what captain can see.
func refuseDuplicateRouteHost(ctx context.Context, client kubernetes.Interface, plan deploy.Plan, namespace string) error {
	list, err := client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil //nolint:nilerr // absence of the check is not a failed check
	}
	for _, existing := range list.Items {
		if existing.Name == plan.IngressName() {
			continue // this agent's own route, being re-applied
		}
		for _, rule := range existing.Spec.Rules {
			if rule.Host != plan.ExternalRoute.Host {
				continue
			}
			return fmt.Errorf(
				"the Ingress %s/%s already routes %s, so this deploy would create a second claim on one host "+
					"and the controller would serve whichever was created first; pick another agent name or "+
					"another --domain", namespace, existing.Name, rule.Host)
		}
	}
	return nil
}

func describeAvailable(names []string) string {
	if len(names) == 0 {
		return "none — no ingress controller is installed"
	}
	return strings.Join(names, ", ")
}

// apiGroupMissing distinguishes "this cluster does not have that API" from any
// other discovery failure.
func apiGroupMissing(err error) bool {
	return strings.Contains(err.Error(), "could not find the requested resource") ||
		strings.Contains(err.Error(), "the server could not find the requested resource") ||
		strings.Contains(strings.ToLower(err.Error()), "not found")
}
