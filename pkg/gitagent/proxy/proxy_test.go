package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/flanksource/commons-db/types"
)

// world spins an upstream recording server, a proxy granting it, and a client
// routed through the proxy.
type world struct {
	upstream  *httptest.Server
	proxy     *httptest.Server
	client    *http.Client
	grant     Grant
	mu        sync.Mutex
	requests  []*http.Request
	headers   []http.Header
	decisions []Decision
}

func newWorld(t *testing.T, secret string) *world {
	t.Helper()
	w := &world{}
	w.upstream = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.requests = append(w.requests, r.Clone(r.Context()))
		w.headers = append(w.headers, r.Header.Clone())
		w.mu.Unlock()
		rw.Header().Set("X-Upstream", "yes")
		rw.WriteHeader(200)
		_, _ = rw.Write([]byte("upstream says hi"))
	}))
	t.Cleanup(w.upstream.Close)

	w.grant = Grant{
		Name:    "gh",
		URL:     w.upstream.URL,
		Methods: []string{"GET", "POST"},
		Paths:   []string{"/repos/acme/"},
		Headers: []HeaderGrant{{Name: "Authorization", Value: types.EnvVar{ValueStatic: secret}}},
	}
	if err := w.grant.Validate(); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{
		Grants: []Grant{w.grant},
		Audit: func(d Decision) {
			w.mu.Lock()
			w.decisions = append(w.decisions, d)
			w.mu.Unlock()
		},
	}
	w.proxy = httptest.NewServer(p)
	t.Cleanup(w.proxy.Close)

	proxyURL, _ := url.Parse(w.proxy.URL)
	w.client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	return w
}

func (w *world) do(t *testing.T, method, target string, headers map[string]string, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (w *world) upstreamHeaderValues(name string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var values []string
	for _, h := range w.headers {
		values = append(values, h.Get(name))
	}
	return values
}

func (w *world) auditDecisions() []Decision {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Decision(nil), w.decisions...)
}

func (w *world) upstreamRequestCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.requests)
}

const secret = "ghp_real_secret_value"

func TestSubstitutesOnlyInTheGrantedPosition(t *testing.T) {
	w := newWorld(t, secret)
	placeholder := w.grant.Placeholder("Authorization")

	resp := w.do(t, "GET", w.upstream.URL+"/repos/acme/captain", map[string]string{"Authorization": placeholder}, "")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "upstream says hi" {
		t.Fatalf("body = %q", body)
	}
	values := w.upstreamHeaderValues("Authorization")
	if len(values) != 1 || values[0] != secret {
		t.Fatalf("upstream saw Authorization = %v, want the real value", values)
	}
}

func TestPlaceholderInNonGrantedHeaderIsRejectedNotStripped(t *testing.T) {
	w := newWorld(t, secret)
	placeholder := w.grant.Placeholder("Authorization")

	resp := w.do(t, "GET", w.upstream.URL+"/repos/acme/captain",
		map[string]string{"X-Exfil": placeholder}, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if n := len(w.upstreamHeaderValues("X-Exfil")); n != 0 {
		t.Fatalf("upstream was contacted %d times; a rejected request must never be forwarded", n)
	}
	found := false
	for _, d := range w.auditDecisions() {
		if d.Verdict == "rejected" && strings.Contains(d.Reason, "R9.2") {
			found = true
			if strings.Contains(d.Reason, secret) || strings.Contains(d.Reason, placeholder) {
				t.Fatal("the audit log must not carry values")
			}
		}
	}
	if !found {
		t.Fatal("the rejection must be logged (R9.5)")
	}
}

func TestPlaceholderInBodyOrURLIsRejected(t *testing.T) {
	w := newWorld(t, secret)
	placeholder := w.grant.Placeholder("Authorization")

	resp := w.do(t, "POST", w.upstream.URL+"/repos/acme/captain", nil, `{"note":"`+placeholder+`"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("body placeholder: status = %d, want 403", resp.StatusCode)
	}
	resp = w.do(t, "GET", w.upstream.URL+"/repos/acme/captain?x="+placeholder, nil, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("url placeholder: status = %d, want 403", resp.StatusCode)
	}
	if w.upstreamRequestCount() != 0 {
		t.Fatal("nothing may reach upstream")
	}
}

func TestNonGrantedDestinationIsDenied(t *testing.T) {
	w := newWorld(t, secret)
	other := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(200)
	}))
	defer other.Close()

	// Including with the placeholder: granted for host A, sent to host B.
	resp := w.do(t, "GET", other.URL+"/anything",
		map[string]string{"Authorization": w.grant.Placeholder("Authorization")}, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (deny by default)", resp.StatusCode)
	}
}

func TestScopeByMethodAndPath(t *testing.T) {
	w := newWorld(t, secret)
	resp := w.do(t, "DELETE", w.upstream.URL+"/repos/acme/captain", nil, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("method outside scope: status = %d, want 403 (R9.6)", resp.StatusCode)
	}
	resp = w.do(t, "POST", w.upstream.URL+"/gists", nil, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("path outside scope: status = %d, want 403 (R9.6/H7)", resp.StatusCode)
	}
	req := httptest.NewRequest("POST", w.upstream.URL+"/repos/acme/../gists", nil)
	recorder := httptest.NewRecorder()
	(&Proxy{Grants: []Grant{w.grant}}).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("dot-segment escape: status = %d, want 403", recorder.Code)
	}
}

func TestUnresolvableCredentialFailsTheRequest(t *testing.T) {
	w := newWorld(t, secret)
	unresolvable := w.grant
	unresolvable.Headers = []HeaderGrant{{Name: "Authorization"}}
	p := &Proxy{Grants: []Grant{unresolvable}}
	broken := httptest.NewServer(p)
	defer broken.Close()
	proxyURL, _ := url.Parse(broken.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	req, _ := http.NewRequest("GET", w.upstream.URL+"/repos/acme/captain", nil)
	req.Header.Set("Authorization", w.grant.Placeholder("Authorization"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (R9.4)", resp.StatusCode)
	}
	for _, v := range w.upstreamHeaderValues("Authorization") {
		if strings.Contains(v, PlaceholderPrefix) {
			t.Fatal("the placeholder must never be forwarded upstream (R9.4)")
		}
	}
}

func TestLaterGrantForSameDestinationCanAuthorize(t *testing.T) {
	w := newWorld(t, secret)
	otherScope := w.grant
	otherScope.Name = "other"
	otherScope.Paths = []string{"/other/"}
	p := &Proxy{Grants: []Grant{otherScope, w.grant}}
	server := httptest.NewServer(p)
	defer server.Close()
	proxyURL, _ := url.Parse(server.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	req, _ := http.NewRequest("GET", w.upstream.URL+"/repos/acme/captain", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestGrantRawHostPreservesExplicitCrossSchemePort(t *testing.T) {
	for _, test := range []struct {
		url  string
		host string
	}{
		{"http://example.com:443", "example.com:443"},
		{"https://example.com:80", "example.com:80"},
	} {
		if got := (Grant{URL: test.url}).rawHost(); got != test.host {
			t.Fatalf("rawHost(%q) = %q, want %q", test.url, got, test.host)
		}
	}
}

func TestProxyReusesTransport(t *testing.T) {
	p := &Proxy{}
	if first, second := p.transport(), p.transport(); first != second {
		t.Fatal("transport was rebuilt")
	}
}

func TestConnectIsRefused(t *testing.T) {
	w := newWorld(t, secret)
	req, _ := http.NewRequest(http.MethodConnect, w.proxy.URL, nil)
	req.URL.Opaque = "example.com:443"
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT: status = %d, want 403", resp.StatusCode)
	}
}

func TestSandboxEnvHoldsOnlyPlaceholders(t *testing.T) {
	w := newWorld(t, secret)
	env := SandboxEnv([]Grant{w.grant})
	if len(env) != 1 {
		t.Fatalf("env = %v", env)
	}
	if env[0] != "GH_AUTHORIZATION="+w.grant.Placeholder("Authorization") {
		t.Fatalf("env = %v", env)
	}
	for _, kv := range env {
		if strings.Contains(kv, secret) {
			t.Fatal("the real secret must be absent from the sandbox environment")
		}
	}
}
