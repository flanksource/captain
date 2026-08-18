package cli

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

// routeOptions is a kubernetes deploy that would render an Ingress.
func routeOptions() GitAgentDeployOptions {
	return GitAgentDeployOptions{
		Name:          "worker-01",
		Target:        "kubernetes",
		Domain:        "agents.example.com",
		IngressClass:  "nginx",
		IngressIssuer: "letsencrypt-prod",
	}
}

func TestApplyExternalRoute(t *testing.T) {
	t.Run("resolves the host the supervisor will dial", func(t *testing.T) {
		plan := deploy.Plan{Name: "worker-01"}
		if err := applyExternalRoute(&plan, routeOptions(), deploy.TargetKubernetes); err != nil {
			t.Fatal(err)
		}
		if plan.ExternalRoute.Host != "worker-01.agents.example.com" {
			t.Fatalf("host = %q", plan.ExternalRoute.Host)
		}
		if plan.ExternalRoute.ClassName != "nginx" || plan.ExternalRoute.ClusterIssuer != "letsencrypt-prod" {
			t.Fatalf("route = %+v", plan.ExternalRoute)
		}
		// The route re-encrypts to the pod, so the pod has to serve TLS. A plan
		// where these disagree accepts the connection and fails the handshake.
		if plan.Transport != string(transportHTTPS) {
			t.Fatalf("transport = %q, want the route's own protocol", plan.Transport)
		}
	})

	// No flags at all is the in-cluster topology, not an error.
	t.Run("leaves the plan alone when no route is asked for", func(t *testing.T) {
		plan := deploy.Plan{Name: "worker-01"}
		if err := applyExternalRoute(&plan, GitAgentDeployOptions{Name: "worker-01"}, deploy.TargetKubernetes); err != nil {
			t.Fatal(err)
		}
		if plan.HasExternalRoute() {
			t.Fatalf("a route was invented: %+v", plan.ExternalRoute)
		}
	})

	t.Run("an operator's own certificate replaces the issuer", func(t *testing.T) {
		opts := routeOptions()
		opts.IngressIssuer = ""
		opts.IngressTLSSecret = "wildcard-agents"

		plan := deploy.Plan{Name: "worker-01"}
		if err := applyExternalRoute(&plan, opts, deploy.TargetKubernetes); err != nil {
			t.Fatal(err)
		}
		if plan.IngressTLSSecretName() != "wildcard-agents" {
			t.Fatalf("secret = %q", plan.IngressTLSSecretName())
		}
	})

	t.Run("Traefik is accepted because captain renders its verified TLS transport", func(t *testing.T) {
		opts := routeOptions()
		opts.IngressClass = "traefik"

		plan := deploy.Plan{Name: "worker-01"}
		if err := applyExternalRoute(&plan, opts, deploy.TargetKubernetes); err != nil {
			t.Fatal(err)
		}
		if !plan.UsesTraefik() {
			t.Fatalf("route = %+v, want Traefik", plan.ExternalRoute)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*GitAgentDeployOptions)
		target deploy.Target
		want   string
	}{{
		name:   "a route on docker",
		mutate: func(o *GitAgentDeployOptions) { o.Target = "docker" },
		target: deploy.TargetDocker, want: "--domain needs --target kubernetes",
	}, {
		name:   "an issuer with no domain",
		mutate: func(o *GitAgentDeployOptions) { o.Domain = "" },
		want:   "has no effect without --domain",
	}, {
		name:   "both certificate sources",
		mutate: func(o *GitAgentDeployOptions) { o.IngressTLSSecret = "wildcard-agents" },
		want:   "mutually exclusive",
	}, {
		// Without either, the controller answers with its own default certificate
		// and the supervisor's push fails verification.
		name:   "neither certificate source",
		mutate: func(o *GitAgentDeployOptions) { o.IngressIssuer = "" },
		want:   "--ingress-issuer",
	}, {
		name:   "no ingress class at all",
		mutate: func(o *GitAgentDeployOptions) { o.IngressClass = "" },
		want:   "--ingress-class",
	}, {
		name:   "a malformed annotation",
		mutate: func(o *GitAgentDeployOptions) { o.IngressAnnotation = []string{"nokey"} },
		want:   "must be key=value",
	}, {
		// The Ingress routes only the host in its rule, so a dispatch to the other
		// name gets a 404 from the controller.
		name:   "an advertise naming a different host",
		mutate: func(o *GitAgentDeployOptions) { o.Advertise = "https://elsewhere.example.com/git/repo.git" },
		want:   "Drop one of them",
	}, {
		// Agent names permit a leading hyphen and 64 characters; DNS does not.
		name: "a name that is not a DNS label",
		mutate: func(o *GitAgentDeployOptions) {
			o.Name = "-worker"
		},
		want: "is not a valid DNS name",
	}, {
		name: "a host longer than DNS allows",
		mutate: func(o *GitAgentDeployOptions) {
			o.Domain = strings.Repeat("a123456789.", 24) + "example.com"
		},
		want: "longer than a DNS name may be",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			opts := routeOptions()
			tc.mutate(&opts)
			target := tc.target
			if target == "" {
				target = deploy.TargetKubernetes
			}
			plan := deploy.Plan{Name: opts.Name}
			err := applyExternalRoute(&plan, opts, target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to name %q", err, tc.want)
			}
		})
	}

	// An advertise naming the same host is redundant rather than contradictory.
	t.Run("an advertise agreeing with the domain is accepted", func(t *testing.T) {
		opts := routeOptions()
		opts.Advertise = "https://worker-01.agents.example.com/git/repo.git"

		plan := deploy.Plan{Name: "worker-01"}
		if err := applyExternalRoute(&plan, opts, deploy.TargetKubernetes); err != nil {
			t.Fatal(err)
		}
	})
}

func TestParseIngressAnnotations(t *testing.T) {
	got, err := parseIngressAnnotations([]string{
		"a=1",
		// A value may itself contain '=', so only the first one splits.
		"b=x=y",
		"a=2",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a": "2", "b": "x=y"}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("annotations[%q] = %q, want %q", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("annotations = %v, want %v", got, want)
	}

	for _, bad := range []string{"nokey", "=value", "  =v"} {
		if _, err := parseIngressAnnotations([]string{bad}); err == nil {
			t.Errorf("parseIngressAnnotations(%q) was accepted", bad)
		}
	}
	// An empty value is legitimate — some controllers read the key's presence.
	if got, err := parseIngressAnnotations([]string{"k="}); err != nil || got["k"] != "" {
		t.Fatalf("k= yielded %v, %v", got, err)
	}
}

func TestResolveExternalHost(t *testing.T) {
	for _, tc := range []struct{ name, domain, want string }{
		{"worker-01", "agents.example.com", "worker-01.agents.example.com"},
		// A trailing dot is how a fully-qualified name is sometimes written; the
		// Ingress rule wants it without.
		{"worker-01", "agents.example.com.", "worker-01.agents.example.com"},
	} {
		got, err := resolveExternalHost(tc.name, tc.domain)
		if err != nil || got != tc.want {
			t.Errorf("resolveExternalHost(%q, %q) = %q, %v; want %q", tc.name, tc.domain, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ name, domain string }{
		{"-worker", "agents.example.com"},
		{"worker-", "agents.example.com"},
		{"worker", "agents..example.com"},
		{"worker", "-agents.example.com"},
	} {
		if _, err := resolveExternalHost(tc.name, tc.domain); err == nil {
			t.Errorf("resolveExternalHost(%q, %q) was accepted", tc.name, tc.domain)
		}
	}
}
