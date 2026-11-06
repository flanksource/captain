package exectool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credproxy"
	"github.com/flanksource/captain/pkg/credproxy/kubeproxy"
)

// caBundleEnv are the variables the common HTTP stacks read a CA bundle from:
// OpenSSL and Go on Linux, curl, Python requests, Node and git.
var caBundleEnv = []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "GIT_SSL_CAINFO"}

type proxyOptions struct {
	dir        string
	baseDir    string
	kubernetes *connection.KubernetesConnection
	grants     []credproxy.Grant
}

// ProxyEndpoints are where a proxied tool reaches its proxies.
type ProxyEndpoints = api.ToolProxyEndpoints

// ServeToolProxies serves a proxied tool's credential proxies from this host,
// for the tool to reach from elsewhere — a sidecar tool, through the tunnel.
// Only what the tool itself declares is proxied: a tool placed elsewhere
// inherits nothing from this run's setup. dir is private to this host and
// holds the materialized kubernetes connection; the closer stops the proxies.
func ServeToolProxies(ctx context.Context, tool api.InlineTool, dir string) (ProxyEndpoints, func() error, error) {
	endpoints, closers, err := serveProxies(dbcontext.NewContext(ctx), proxyOptions{
		dir: dir, baseDir: tool.BaseDir, kubernetes: tool.Connections.Kubernetes, grants: tool.Grants,
	})
	closeAll := func() error {
		var errs []error
		for i := len(closers) - 1; i >= 0; i-- {
			errs = append(errs, closers[i]())
		}
		return errors.Join(errs...)
	}
	if err != nil {
		return ProxyEndpoints{}, nil, errors.Join(err, closeAll())
	}
	return endpoints, closeAll, nil
}

// serveProxies starts the proxies a proxied tool reaches its credentials
// through, where the credentials are. The kubernetes connection is
// materialized under dir for the kube proxy alone, and grant values are
// resolved per request by the egress proxy; both stay in this process.
func serveProxies(ctx dbcontext.Context, opts proxyOptions) (ProxyEndpoints, []func() error, error) {
	var endpoints ProxyEndpoints
	var closers []func() error
	if opts.kubernetes != nil {
		kube, kubeClosers, err := serveKubeProxy(ctx, opts)
		closers = append(closers, kubeClosers...)
		if err != nil {
			return endpoints, closers, err
		}
		endpoints.KubeURL, endpoints.KubeToken = kube.URL, kube.Token
	}
	if len(opts.grants) > 0 {
		resolve := func(value types.EnvVar) (string, error) {
			return ctx.GetEnvValueFromCache(value, ctx.GetNamespace())
		}
		egress, closer, err := ServeEgressProxy(opts.grants, resolve)
		if closer != nil {
			closers = append(closers, closer)
		}
		if err != nil {
			return endpoints, closers, err
		}
		endpoints.Egress, endpoints.EgressSecret, endpoints.CAPEM = egress.Egress, egress.EgressSecret, egress.CAPEM
	}
	return endpoints, closers, nil
}

// serveKubeProxy materializes the kubernetes connection into a private
// kubeconfig and serves it through a token-guarded, auth-injecting proxy.
func serveKubeProxy(ctx dbcontext.Context, opts proxyOptions) (kubeproxy.Endpoint, []func() error, error) {
	materialized, err := shell.SetupEnv(ctx, &shell.Exec{
		Connections:    connection.ExecConnections{Kubernetes: opts.kubernetes},
		Chroot:         opts.dir,
		BaseDir:        opts.baseDir,
		CredentialsDir: filepath.Join(opts.dir, "kube"),
	})
	if err != nil {
		return kubeproxy.Endpoint{}, nil, fmt.Errorf("kubernetes connection: %w", err)
	}
	closers := []func() error{materialized.Cleanup}
	kubeconfig := envValue(materialized.Env, "KUBECONFIG")
	if kubeconfig == "" {
		// An empty path would make the proxy fall back to the host's own
		// kubeconfig — a credential nobody granted this tool.
		return kubeproxy.Endpoint{}, closers, fmt.Errorf("kubernetes connection produced no KUBECONFIG to proxy")
	}
	token, err := credproxy.NewSecret()
	if err != nil {
		return kubeproxy.Endpoint{}, closers, err
	}
	proxy, err := kubeproxy.Start(kubeproxy.Options{Kubeconfig: kubeconfig, Token: token})
	if err != nil {
		return kubeproxy.Endpoint{}, closers, fmt.Errorf("kube proxy: %w", err)
	}
	return proxy.Endpoint(), append(closers, func() error { proxy.Close(); return nil }), nil
}

// ServeEgressProxy serves grants on a loopback port behind a fresh proxy
// secret. HTTPS reaches it through CONNECT, which the proxy terminates with a
// per-run CA; the CA's key never leaves this process. resolve turns a granted
// header's value into the real credential, per request.
func ServeEgressProxy(grants []credproxy.Grant, resolve credproxy.Resolver) (ProxyEndpoints, func() error, error) {
	authority, err := credproxy.NewAuthority()
	if err != nil {
		return ProxyEndpoints{}, nil, err
	}
	secret, err := credproxy.NewSecret()
	if err != nil {
		return ProxyEndpoints{}, nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return ProxyEndpoints{}, nil, fmt.Errorf("egress proxy: %w", err)
	}
	server := &http.Server{
		Handler: &credproxy.Proxy{
			Grants:    grants,
			Authority: authority,
			Secret:    secret,
			Resolve:   resolve,
			Audit:     auditDecision,
		},
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	return ProxyEndpoints{Egress: listener.Addr().String(), EgressSecret: secret, CAPEM: authority.CertPEM()}, server.Close, nil
}

// ProxiedEnv is the credential-free environment pointing a tool at its
// proxies: a kubeconfig naming only the kube proxy, the proxy variables with
// the egress secret as userinfo, the CA bundle variables, and one placeholder
// per granted header. Files land in dir, which must be private to the tool.
func ProxiedEnv(dir string, endpoints ProxyEndpoints, grants []credproxy.Grant) ([]string, error) {
	var env, noProxy []string
	if endpoints.KubeURL != "" {
		kubeconfig, err := kubeproxy.WriteKubeconfig(filepath.Join(dir, "proxy"), kubeproxy.Endpoint{URL: endpoints.KubeURL, Token: endpoints.KubeToken})
		if err != nil {
			return nil, fmt.Errorf("kube proxy kubeconfig: %w", err)
		}
		server, err := url.Parse(endpoints.KubeURL)
		if err != nil {
			return nil, err
		}
		env = append(env, "KUBECONFIG="+kubeconfig)
		// curl matches NO_PROXY entries against the host alone; a host:port
		// entry never matches and would send kube traffic to the egress proxy.
		noProxy = append(noProxy, server.Hostname())
	}
	if endpoints.Egress == "" {
		return env, nil
	}
	caFile := filepath.Join(dir, "proxy-ca.pem")
	if err := os.WriteFile(caFile, endpoints.CAPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write proxy CA: %w", err)
	}
	proxyURL := credproxy.ProxyURL(endpoints.Egress, endpoints.EgressSecret)
	env = append(env, "HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL, "http_proxy="+proxyURL, "https_proxy="+proxyURL)
	if len(noProxy) > 0 {
		value := strings.Join(noProxy, ",")
		env = append(env, "NO_PROXY="+value, "no_proxy="+value)
	}
	for _, name := range caBundleEnv {
		env = append(env, name+"="+caFile)
	}
	return append(env, credproxy.SandboxEnv(grants)...), nil
}

// auditDecision logs what the egress proxy did. A rejection is a tool trying
// something its grants do not allow — possibly an exfiltration attempt — so it
// is a warning; a forwarded request is routine. Decisions never carry values.
func auditDecision(decision credproxy.Decision) {
	if decision.Verdict == "forwarded" {
		logger.V(3).Infof("inline tool proxy: %s %s %s (%s)", decision.Verdict, decision.Method, decision.Destination, decision.Reason)
		return
	}
	logger.Warnf("inline tool proxy: %s %s %s: %s", decision.Verdict, decision.Method, decision.Destination, decision.Reason)
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if name, value, ok := strings.Cut(env[i], "="); ok && name == key {
			return value
		}
	}
	return ""
}
