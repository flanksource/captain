package exectool

import (
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

// startProxies starts the host-side proxies a proxied tool reaches its
// credentials through, returning the credential-free environment that points
// the tool at them and the closers that stop them. The real credentials stay in
// this process: the kubernetes connection is materialized under dir for the
// kube proxy alone, and grant values are resolved per request by the egress
// proxy.
func startProxies(ctx dbcontext.Context, opts proxyOptions) ([]string, []func() error, error) {
	var env, noProxy []string
	var closers []func() error
	if opts.kubernetes != nil {
		kubeEnv, kubeAddr, kubeClosers, err := startKubeProxy(ctx, opts)
		closers = append(closers, kubeClosers...)
		if err != nil {
			return nil, closers, err
		}
		env = append(env, kubeEnv...)
		noProxy = append(noProxy, kubeAddr)
	}
	if len(opts.grants) > 0 {
		egressEnv, closer, err := startEgressProxy(ctx, opts, noProxy)
		if closer != nil {
			closers = append(closers, closer)
		}
		if err != nil {
			return nil, closers, err
		}
		env = append(env, egressEnv...)
	}
	return env, closers, nil
}

// startKubeProxy materializes the kubernetes connection into a private
// kubeconfig and serves it through an auth-injecting reverse proxy. The tool
// gets a kubeconfig naming only the proxy, with no user credentials.
func startKubeProxy(ctx dbcontext.Context, opts proxyOptions) ([]string, string, []func() error, error) {
	realDir := filepath.Join(opts.dir, "kube")
	materialized, err := shell.SetupEnv(ctx, &shell.Exec{
		Connections:    connection.ExecConnections{Kubernetes: opts.kubernetes},
		Chroot:         opts.dir,
		BaseDir:        opts.baseDir,
		CredentialsDir: realDir,
	})
	if err != nil {
		return nil, "", nil, fmt.Errorf("kubernetes connection: %w", err)
	}
	closers := []func() error{materialized.Cleanup}
	kubeconfig := envValue(materialized.Env, "KUBECONFIG")
	if kubeconfig == "" {
		// An empty path would make the proxy fall back to the host's own
		// kubeconfig — a credential nobody granted this tool.
		return nil, "", closers, fmt.Errorf("kubernetes connection produced no KUBECONFIG to proxy")
	}
	proxy, err := kubeproxy.Start(kubeconfig)
	if err != nil {
		return nil, "", closers, fmt.Errorf("kube proxy: %w", err)
	}
	closers = append(closers, func() error { proxy.Close(); return nil })
	proxied, err := proxy.WriteKubeconfig(filepath.Join(opts.dir, "proxy"))
	if err != nil {
		return nil, "", closers, fmt.Errorf("kube proxy kubeconfig: %w", err)
	}
	server, err := url.Parse(proxy.URL())
	if err != nil {
		return nil, "", closers, err
	}
	return []string{"KUBECONFIG=" + proxied}, server.Host, closers, nil
}

// startEgressProxy serves the tool's grants on a loopback port. HTTPS reaches
// it through CONNECT, which the proxy terminates with a per-run CA the tool is
// told to trust; the CA's key never leaves this process.
func startEgressProxy(ctx dbcontext.Context, opts proxyOptions, noProxy []string) ([]string, func() error, error) {
	authority, err := credproxy.NewAuthority()
	if err != nil {
		return nil, nil, err
	}
	caFile := filepath.Join(opts.dir, "proxy-ca.pem")
	if err := os.WriteFile(caFile, authority.CertPEM(), 0o600); err != nil {
		return nil, nil, fmt.Errorf("write proxy CA: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("egress proxy: %w", err)
	}
	server := &http.Server{
		Handler: &credproxy.Proxy{
			Grants:    opts.grants,
			Authority: authority,
			Resolve: func(value types.EnvVar) (string, error) {
				return ctx.GetEnvValueFromCache(value, ctx.GetNamespace())
			},
			Audit: auditDecision,
		},
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	proxyURL := "http://" + listener.Addr().String()
	env := []string{"HTTP_PROXY=" + proxyURL, "HTTPS_PROXY=" + proxyURL, "http_proxy=" + proxyURL, "https_proxy=" + proxyURL}
	if len(noProxy) > 0 {
		value := strings.Join(noProxy, ",")
		env = append(env, "NO_PROXY="+value, "no_proxy="+value)
	}
	for _, name := range caBundleEnv {
		env = append(env, name+"="+caFile)
	}
	return append(env, credproxy.SandboxEnv(opts.grants)...), server.Close, nil
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
