// Caller-tool talkback keeps task capabilities out of the Git protocol. The
// authenticated supervisor delivers one grant to an HTTPS sidecar, which
// briefly materializes it as a task-local secret and proxies MCP requests back
// to the supervisor without ever using the durable Git transport token as tool
// authority.
package gitagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
)

const (
	CallerToolPath          = "/api/v1/caller-tools"
	callerToolSecretFile    = "caller-tools.json"
	maxCallerToolGrantBytes = 64 << 10
)

// CallerToolGrant is delivered over the sidecar's authenticated HTTPS control
// path. Endpoint headers are plaintext capability material and are never JSON
// marshaled directly by callers.
type CallerToolGrant struct {
	Task      string
	Agent     string
	Endpoint  api.CallerToolEndpoint `json:"-"`
	ExpiresAt time.Time
}

type callerToolGrantWire struct {
	Task       string    `json:"task"`
	Agent      string    `json:"agent"`
	Name       string    `json:"name"`
	Route      string    `json:"route"`
	Credential string    `json:"credential"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type callerToolEndpointSecret struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// CallerToolProxyConfig binds a sidecar proxy to its enrolled supervisor and
// standard Captain task runner.
type CallerToolProxyConfig struct {
	Root                string
	EndpointURL         string
	SupervisorURL       string
	SupervisorCAPath    string
	SupervisorPublicKey string
	Agent               string
	DefaultRunner       bool
	IdentifySupervisor  func(*http.Request) (string, error)
	Log                 func(format string, args ...any)
}

type callerToolProxy struct {
	root               string
	endpointURL        *url.URL
	supervisorURL      *url.URL
	client             *http.Client
	agent              string
	defaultRunner      bool
	identifySupervisor func(*http.Request) (string, error)
	log                func(format string, args ...any)

	mu       sync.RWMutex
	sessions map[string]*callerToolSession
}

type callerToolSession struct {
	task      string
	agent     string
	route     string
	tokenHash [sha256.Size]byte
	expiresAt time.Time
	revoked   atomic.Bool
}

// NewCallerToolProxy creates the sidecar handler used for authenticated grant
// delivery and task-scoped MCP proxying.
func NewCallerToolProxy(config CallerToolProxyConfig) (http.Handler, error) {
	if strings.TrimSpace(config.Root) == "" || strings.TrimSpace(config.Agent) == "" || config.IdentifySupervisor == nil {
		return nil, fmt.Errorf("caller-tool proxy requires a sidecar root, enrolled agent, and supervisor identity resolver")
	}
	endpoint, err := parseCallerToolBase(config.EndpointURL)
	if err != nil {
		return nil, err
	}
	supervisor, err := parseHTTPSBase(config.SupervisorURL, "enrolled supervisor URL")
	if err != nil {
		return nil, err
	}
	client, err := HTTPSClient(config.SupervisorCAPath, config.SupervisorPublicKey)
	if err != nil {
		return nil, err
	}
	logf := config.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	removed, err := removeStaleCallerToolSecrets(config.Root)
	if err != nil {
		return nil, err
	}
	if removed > 0 {
		logf("git-agent caller-tool startup removed %d stale task secret(s)", removed)
	}
	proxy := &callerToolProxy{
		root: config.Root, endpointURL: endpoint, supervisorURL: supervisor,
		client: client, agent: config.Agent, defaultRunner: config.DefaultRunner,
		identifySupervisor: config.IdentifySupervisor, log: logf,
		sessions: map[string]*callerToolSession{},
	}
	return http.HandlerFunc(proxy.serveHTTP), nil
}

func (proxy *callerToolProxy) serveHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path == CallerToolPath {
		if request.Method == http.MethodPost {
			proxy.register(w, request)
			return
		}
		http.NotFound(w, request)
		return
	}
	trimmed, ok := strings.CutPrefix(request.URL.Path, CallerToolPath+"/")
	if !ok {
		http.NotFound(w, request)
		return
	}
	parts := strings.Split(trimmed, "/")
	switch {
	case len(parts) == 2 && parts[1] == "mcp":
		proxy.forward(w, request, parts[0])
	case len(parts) == 1 && request.Method == http.MethodDelete:
		proxy.revoke(w, request, parts[0])
	default:
		http.NotFound(w, request)
	}
}

func (proxy *callerToolProxy) register(w http.ResponseWriter, request *http.Request) {
	if _, err := proxy.identifySupervisor(request); err != nil {
		http.Error(w, "caller-tool grant requires the enrolled supervisor credential", http.StatusForbidden)
		return
	}
	if !proxy.defaultRunner {
		http.Error(w, "delegated caller tools require Captain's default git-agent task runner", http.StatusConflict)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxCallerToolGrantBytes+1))
	if err != nil || len(payload) > maxCallerToolGrantBytes {
		http.Error(w, "caller-tool grant is too large", http.StatusRequestEntityTooLarge)
		return
	}
	var grant callerToolGrantWire
	if err := json.Unmarshal(payload, &grant); err != nil {
		http.Error(w, "invalid caller-tool grant", http.StatusBadRequest)
		return
	}
	if err := validateCallerToolGrant(grant); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if grant.Agent != proxy.agent {
		http.Error(w, "caller-tool grant is bound to a different agent", http.StatusForbidden)
		return
	}
	endpointURL := *proxy.endpointURL
	endpointURL.Path = CallerToolPath + "/" + grant.Task + "/mcp"
	endpointURL.RawPath, endpointURL.RawQuery, endpointURL.Fragment = "", "", ""
	secret := callerToolEndpointSecret{
		Name: grant.Name, URL: endpointURL.String(),
		Headers: map[string]string{
			"Authorization":         "Bearer " + grant.Credential,
			callertools.TaskHeader:  grant.Task,
			callertools.AgentHeader: grant.Agent,
		},
	}
	session := &callerToolSession{
		task: grant.Task, agent: grant.Agent, route: grant.Route,
		tokenHash: sha256.Sum256([]byte(grant.Credential)), expiresAt: grant.ExpiresAt,
	}
	proxy.mu.Lock()
	var expired *callerToolSession
	if existing := proxy.sessions[grant.Task]; existing != nil {
		if !existing.revoked.Load() && time.Now().Before(existing.expiresAt) {
			proxy.mu.Unlock()
			http.Error(w, "caller-tool grant already exists for task", http.StatusConflict)
			return
		}
		delete(proxy.sessions, grant.Task)
		if existing.revoked.CompareAndSwap(false, true) {
			expired = existing
		}
		if err := removeCallerToolSecret(proxy.root, grant.Task); err != nil {
			proxy.mu.Unlock()
			http.Error(w, "remove expired caller-tool task secret", http.StatusInternalServerError)
			return
		}
	}
	if err := writeCallerToolSecret(proxy.root, grant.Task, secret); err != nil {
		proxy.mu.Unlock()
		http.Error(w, "store caller-tool task secret", http.StatusInternalServerError)
		return
	}
	proxy.sessions[grant.Task] = session
	proxy.mu.Unlock()
	if expired != nil {
		proxy.log("git-agent caller-tool grant expired task=%s agent=%s", expired.task, expired.agent)
	}
	time.AfterFunc(time.Until(grant.ExpiresAt), func() { proxy.expire(session) })
	proxy.log("git-agent caller-tool grant issued task=%s agent=%s expires=%s",
		grant.Task, grant.Agent, grant.ExpiresAt.UTC().Format(time.RFC3339))
	w.WriteHeader(http.StatusNoContent)
}

func (proxy *callerToolProxy) forward(w http.ResponseWriter, request *http.Request, task string) {
	if ValidateTaskID(task) != nil {
		writeCallerToolRejection(w)
		return
	}
	proxy.mu.RLock()
	session := proxy.sessions[task]
	proxy.mu.RUnlock()
	if session == nil || session.revoked.Load() {
		writeCallerToolRejection(w)
		return
	}
	if !time.Now().Before(session.expiresAt) {
		proxy.expire(session)
		writeCallerToolRejection(w)
		return
	}
	if strings.TrimSpace(request.Header.Get("Origin")) != "" {
		http.Error(w, "caller-tool endpoint does not accept browser origins", http.StatusForbidden)
		return
	}
	presented, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	hash := sha256.Sum256([]byte(strings.TrimSpace(presented)))
	if !ok || subtle.ConstantTimeCompare(hash[:], session.tokenHash[:]) != 1 {
		proxy.log("git-agent caller-tool request denied task=%s agent=%s reason=invalid_credential", session.task, session.agent)
		writeCallerToolRejection(w)
		return
	}
	if request.Header.Get(callertools.TaskHeader) != session.task ||
		request.Header.Get(callertools.AgentHeader) != session.agent {
		proxy.log("git-agent caller-tool request denied task=%s agent=%s reason=binding_mismatch", session.task, session.agent)
		http.Error(w, "caller-tool capability binding does not match this task", http.StatusForbidden)
		return
	}
	upstreamURL := *proxy.supervisorURL
	upstreamURL.Path = session.route
	upstreamURL.RawPath, upstreamURL.RawQuery, upstreamURL.Fragment = "", request.URL.RawQuery, ""
	reverseProxy := &httputil.ReverseProxy{
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			proxyRequest.Out.URL = &upstreamURL
			proxyRequest.Out.Host = upstreamURL.Host
			proxyRequest.Out.Header.Set(callertools.TaskHeader, session.task)
			proxyRequest.Out.Header.Set(callertools.AgentHeader, session.agent)
		},
		Transport:     proxy.client.Transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			proxy.log("git-agent caller-tool proxy failed task=%s agent=%s", session.task, session.agent)
			http.Error(w, "caller-tool supervisor is unavailable", http.StatusBadGateway)
		},
	}
	reverseProxy.ServeHTTP(w, request)
}

func (proxy *callerToolProxy) revoke(w http.ResponseWriter, request *http.Request, task string) {
	if _, err := proxy.identifySupervisor(request); err != nil {
		http.Error(w, "caller-tool revocation requires the enrolled supervisor credential", http.StatusForbidden)
		return
	}
	if err := ValidateTaskID(task); err != nil {
		http.NotFound(w, request)
		return
	}
	proxy.mu.Lock()
	session := proxy.sessions[task]
	delete(proxy.sessions, task)
	proxy.mu.Unlock()
	if session != nil && session.revoked.CompareAndSwap(false, true) {
		proxy.log("git-agent caller-tool grant revoked task=%s agent=%s", session.task, session.agent)
	}
	if err := removeCallerToolSecret(proxy.root, task); err != nil {
		proxy.log("git-agent caller-tool secret cleanup failed task=%s: %v", task, err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (proxy *callerToolProxy) expire(session *callerToolSession) {
	if session.revoked.CompareAndSwap(false, true) {
		removed := false
		proxy.mu.Lock()
		if proxy.sessions[session.task] == session {
			delete(proxy.sessions, session.task)
			removed = true
		}
		proxy.mu.Unlock()
		proxy.log("git-agent caller-tool grant expired task=%s agent=%s", session.task, session.agent)
		if removed {
			if err := removeCallerToolSecret(proxy.root, session.task); err != nil {
				proxy.log("git-agent caller-tool secret cleanup failed task=%s agent=%s: %v", session.task, session.agent, err)
			}
		}
	}
}

// RegisterCallerTools delivers a task capability using the durable dispatch
// credential only to authenticate the delivery channel.
func RegisterCallerTools(ctx context.Context, target TransportTarget, grant CallerToolGrant) error {
	wire, err := callerToolGrantToWire(grant)
	if err != nil {
		return err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	request, err := newCallerToolControlRequest(ctx, http.MethodPost, target, "", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client, err := HTTPSClient(target.CAPath, target.PinnedPublicKey)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	return doCallerToolControl(client, request)
}

// RevokeCallerTools removes any unconsumed task secret and disables the
// sidecar proxy session. Supervisor-side revocation remains authoritative if
// this best-effort cleanup cannot reach the sidecar.
func RevokeCallerTools(ctx context.Context, target TransportTarget, task string) error {
	request, err := newCallerToolControlRequest(ctx, http.MethodDelete, target, "/"+task, nil)
	if err != nil {
		return err
	}
	client, err := HTTPSClient(target.CAPath, target.PinnedPublicKey)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	return doCallerToolControl(client, request)
}

// LoadCallerToolEndpoint consumes the sidecar-delivered secret before the
// remote model process starts.
func LoadCallerToolEndpoint(sidecarRepo, task string) (*api.CallerToolEndpoint, error) {
	name, err := callerToolSecretName(task)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(sidecarRepo)
	if err != nil {
		return nil, fmt.Errorf("open caller-tool secret store: %w", err)
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		return nil, fmt.Errorf("caller-tool task secret: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("caller-tool task secret must not be accessible by group or other users")
	}
	payload, err := root.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if err := root.Remove(name); err != nil {
		return nil, fmt.Errorf("consume caller-tool task secret: %w", err)
	}
	var secret callerToolEndpointSecret
	if err := json.Unmarshal(payload, &secret); err != nil {
		return nil, fmt.Errorf("decode caller-tool task secret: %w", err)
	}
	endpoint := &api.CallerToolEndpoint{Name: secret.Name, URL: secret.URL, Headers: secret.Headers}
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	return endpoint, nil
}

func callerToolGrantToWire(grant CallerToolGrant) (callerToolGrantWire, error) {
	if err := ValidateTaskID(grant.Task); err != nil {
		return callerToolGrantWire{}, err
	}
	parsed, err := url.Parse(grant.Endpoint.URL)
	if err != nil {
		return callerToolGrantWire{}, err
	}
	credential, ok := strings.CutPrefix(grant.Endpoint.Headers["Authorization"], "Bearer ")
	if !ok || strings.TrimSpace(credential) == "" {
		return callerToolGrantWire{}, fmt.Errorf("delegated caller-tool endpoint has no bearer credential")
	}
	if grant.Endpoint.Headers[callertools.TaskHeader] != grant.Task ||
		grant.Endpoint.Headers[callertools.AgentHeader] != grant.Agent {
		return callerToolGrantWire{}, fmt.Errorf("delegated caller-tool endpoint binding does not match its task and agent")
	}
	return callerToolGrantWire{
		Task: grant.Task, Agent: grant.Agent, Name: grant.Endpoint.Name,
		Route: parsed.EscapedPath(), Credential: strings.TrimSpace(credential), ExpiresAt: grant.ExpiresAt,
	}, nil
}

func validateCallerToolGrant(grant callerToolGrantWire) error {
	if err := ValidateTaskID(grant.Task); err != nil {
		return err
	}
	if strings.TrimSpace(grant.Agent) == "" || strings.TrimSpace(grant.Name) == "" || strings.TrimSpace(grant.Credential) == "" {
		return fmt.Errorf("caller-tool grant requires agent, endpoint name, and credential")
	}
	trimmed, ok := strings.CutPrefix(grant.Route, callertools.RemoteEndpointPrefix)
	parts := strings.Split(trimmed, "/")
	if !ok || len(parts) != 2 || parts[0] == "" || parts[1] != "mcp" {
		return fmt.Errorf("caller-tool grant route is not a Captain remote endpoint")
	}
	if !grant.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("caller-tool grant has expired")
	}
	return nil
}

func newCallerToolControlRequest(
	ctx context.Context,
	method string,
	target TransportTarget,
	suffix string,
	body io.Reader,
) (*http.Request, error) {
	if EndpointScheme(target.URL) != "https" {
		return nil, fmt.Errorf("delegated caller tools require an HTTPS git-agent endpoint, got %s", target.URL)
	}
	if target.Token.IsEmpty() {
		return nil, fmt.Errorf("delegated caller tools require the sidecar's authenticated dispatch channel")
	}
	parsed, err := url.Parse(target.URL)
	if err != nil {
		return nil, err
	}
	parsed.Path = CallerToolPath + suffix
	parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", ""
	request, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+target.Token.Value())
	return request, nil
}

func doCallerToolControl(client *http.Client, request *http.Request) error {
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("caller-tool sidecar exchange: %w", err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, maxCallerToolGrantBytes))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("caller-tool sidecar exchange returned %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	return nil
}

// writeCallerToolSecret confines the validated task-relative name with
// os.Root, so a sidecar-state symlink cannot redirect capability material.
func writeCallerToolSecret(root, task string, endpoint callerToolEndpointSecret) error {
	name, err := callerToolSecretName(task)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(endpoint)
	if err != nil {
		return err
	}
	store, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	file, err := store.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	created := true
	defer func() {
		if created {
			_ = store.Remove(name)
		}
	}()
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	dir, err := store.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return err
	}
	created = false
	return nil
}

func removeCallerToolSecret(root, task string) error {
	name, err := callerToolSecretName(task)
	if err != nil {
		return err
	}
	store, err := os.OpenRoot(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer store.Close()
	err = store.Remove(name)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func callerToolSecretName(task string) (string, error) {
	if err := ValidateTaskID(task); err != nil {
		return "", err
	}
	return filepath.Join("captain", "tasks", task, callerToolSecretFile), nil
}

// removeStaleCallerToolSecrets fails closed after a sidecar restart: in-memory
// proxy sessions cannot be recovered, so their unconsumed endpoint files must
// not outlive the process that authenticated them.
func removeStaleCallerToolSecrets(root string) (int, error) {
	store, err := os.OpenRoot(root)
	if err != nil {
		return 0, fmt.Errorf("open caller-tool secret store: %w", err)
	}
	defer store.Close()
	tasks := filepath.Join("captain", "tasks")
	dir, err := store.Open(tasks)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("scan stale caller-tool task secrets: %w", err)
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return 0, fmt.Errorf("scan stale caller-tool task secrets: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || ValidateTaskID(entry.Name()) != nil {
			continue
		}
		err := store.Remove(filepath.Join(tasks, entry.Name(), callerToolSecretFile))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return removed, fmt.Errorf("remove stale caller-tool task secret for %s: %w", entry.Name(), err)
		}
		removed++
	}
	return removed, nil
}

func parseHTTPSBase(raw, name string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("%s %q must use https://", name, raw)
	}
	return parsed, nil
}

func parseCallerToolBase(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("caller-tool endpoint base %q must be an absolute URL", raw)
	}
	probe := *parsed
	probe.Path = CallerToolPath + "/probe/mcp"
	probe.RawPath, probe.RawQuery, probe.Fragment = "", "", ""
	if err := (api.CallerToolEndpoint{
		Name: "captain", URL: probe.String(),
		Headers: map[string]string{"Authorization": "Bearer probe"},
	}).Validate(); err != nil {
		return nil, err
	}
	return parsed, nil
}

func writeCallerToolRejection(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "invalid caller-tool credential", http.StatusUnauthorized)
}
