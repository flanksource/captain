package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// maxScannedBody bounds how much request body is scanned for misplaced
// placeholders. Response bodies are deliberately not scanned: response-body
// redaction is telemetry, not a control (§9 non-goal).
const maxScannedBody = 1 << 20

// Decision is one audit record. Values are never logged (R9.5).
type Decision struct {
	Time        time.Time
	Method      string
	Destination string
	Header      string // the substituted or offending header, when relevant
	Verdict     string // forwarded | rejected | error
	Reason      string
}

// Proxy is the egress credential proxy: an HTTP forward proxy that
// substitutes placeholders in exactly the granted position and rejects them
// anywhere else. CONNECT is refused — tunneled TLS would blind the proxy, and
// the sandbox has no legitimate need to hide its requests from it.
type Proxy struct {
	Grants  []Grant
	Resolve Resolver
	// Audit receives every decision; nil discards. Never receives a value.
	Audit func(Decision)
	// Dialer resolves and dials upstream. The proxy resolves DNS itself and
	// the TLS layer validates the certificate against the granted host name —
	// never the sandbox-controlled Host header or SNI (R9.1).
	Dialer *net.Dialer
	once   sync.Once
	rt     *http.Transport
}

func (p *Proxy) audit(d Decision) {
	d.Time = time.Now()
	if p.Audit != nil {
		p.Audit(d)
	}
}

func (p *Proxy) resolve(v HeaderGrant) (string, error) {
	resolver := p.Resolve
	if resolver == nil {
		resolver = StaticResolver
	}
	return resolver(v.Value)
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.audit(Decision{Method: r.Method, Destination: r.Host, Verdict: "rejected", Reason: "CONNECT is not served; requests must be inspectable"})
		http.Error(w, "captain-proxy: CONNECT is not served", http.StatusForbidden)
		return
	}
	if !r.URL.IsAbs() {
		p.audit(Decision{Method: r.Method, Destination: r.Host, Verdict: "rejected", Reason: "not an absolute-form proxy request"})
		http.Error(w, "captain-proxy: absolute-form proxy requests only", http.StatusBadRequest)
		return
	}
	canonicalPath, canonical := canonicalRequestPath(r.URL.Path)
	if !canonical {
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "rejected", Reason: "request path is not canonical (R9.6)"})
		http.Error(w, "captain-proxy: request path is not canonical", http.StatusForbidden)
		return
	}
	grants := p.grantsFor(r.URL.Hostname(), r.URL.Port(), r.URL.Scheme)
	if len(grants) == 0 {
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "rejected", Reason: "destination not granted (deny by default)"})
		http.Error(w, "captain-proxy: destination not granted", http.StatusForbidden)
		return
	}
	grant, ok := scopedGrantFor(grants, r, canonicalPath)
	if !ok {
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "rejected", Reason: "method or path outside the grant's scope (R9.6)"})
		http.Error(w, "captain-proxy: method or path outside the grant's scope", http.StatusForbidden)
		return
	}
	if reason := p.findMisplacedPlaceholder(r, grant); reason != "" {
		// Rejected and logged, never stripped-and-forwarded: the appearance
		// is an exfiltration attempt and silently continuing hides it (R9.2).
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "rejected", Reason: reason})
		http.Error(w, "captain-proxy: "+reason, http.StatusForbidden)
		return
	}
	substituted, err := p.substitute(r, grant)
	if err != nil {
		// A credential that fails to resolve fails the request loudly; the
		// placeholder is never forwarded upstream (R9.4).
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "error", Reason: err.Error()})
		http.Error(w, "captain-proxy: credential resolution failed", http.StatusBadGateway)
		return
	}
	p.forward(w, r, grant, substituted)
}

// grantsFor returns every grant for a destination. Scope selection happens
// separately so one destination can carry independent capabilities.
func (p *Proxy) grantsFor(host, port, scheme string) []Grant {
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	var matches []Grant
	for _, g := range p.Grants {
		if g.host() == net.JoinHostPort(host, port) && g.scheme() == scheme {
			matches = append(matches, g)
		}
	}
	return matches
}

func scopedGrantFor(grants []Grant, r *http.Request, requestPath string) (Grant, bool) {
	var scoped []Grant
	for _, grant := range grants {
		if grant.AllowsMethod(r.Method) && grant.AllowsPath(requestPath) {
			scoped = append(scoped, grant)
		}
	}
	for _, grant := range scoped {
		for _, header := range grant.Headers {
			if r.Header.Get(header.Name) == grant.Placeholder(header.Name) {
				return grant, true
			}
		}
	}
	if len(scoped) > 0 {
		return scoped[0], true
	}
	return Grant{}, false
}

func canonicalRequestPath(requestPath string) (string, bool) {
	if requestPath == "" {
		requestPath = "/"
	}
	cleaned := path.Clean(requestPath)
	if strings.HasSuffix(requestPath, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned, cleaned == requestPath
}

// findMisplacedPlaceholder scans headers, URL and a bounded body prefix for
// any placeholder that is not this grant's placeholder in this grant's
// header. It returns the rejection reason, or "".
func (p *Proxy) findMisplacedPlaceholder(r *http.Request, grant Grant) string {
	if strings.Contains(r.URL.String(), PlaceholderPrefix) {
		return "credential placeholder in the URL is an exfiltration attempt (R9.2)"
	}
	for name, values := range r.Header {
		for _, value := range values {
			if !strings.Contains(value, PlaceholderPrefix) {
				continue
			}
			granted, ok := grant.grantedHeader(name)
			if !ok || value != grant.Placeholder(granted.Name) {
				return fmt.Sprintf("credential placeholder in header %s is not granted for this destination (R9.2)", name)
			}
		}
	}
	if r.Body != nil {
		var scanned bytes.Buffer
		_, _ = scanned.ReadFrom(io.LimitReader(r.Body, maxScannedBody))
		body := scanned.Bytes()
		rest := r.Body
		r.Body = readCloser{io.MultiReader(bytes.NewReader(body), rest), rest}
		if bytes.Contains(body, []byte(PlaceholderPrefix)) {
			return "credential placeholder in the request body is an exfiltration attempt (R9.2)"
		}
	}
	return ""
}

// substitute swaps each granted header's placeholder for its resolved value,
// returning the headers it substituted.
func (p *Proxy) substitute(r *http.Request, grant Grant) ([]string, error) {
	var substituted []string
	for _, h := range grant.Headers {
		value := r.Header.Get(h.Name)
		if value != grant.Placeholder(h.Name) {
			continue // header absent or carrying the caller's own value
		}
		real, err := p.resolve(h)
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", h.Name, err)
		}
		r.Header.Set(h.Name, real)
		substituted = append(substituted, h.Name)
	}
	return substituted, nil
}

// forward relays the request upstream, resolving DNS itself and letting TLS
// validate against the granted host name (R9.1).
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, grant Grant, substituted []string) {
	transport := p.transport()

	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL.Scheme = grant.scheme()
	out.URL.Host = grant.rawHost()
	out.Host = out.URL.Host

	resp, err := transport.RoundTrip(out)
	if err != nil {
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "error", Reason: "upstream: " + err.Error()})
		http.Error(w, "captain-proxy: upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, name := range substituted {
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Header: name, Verdict: "forwarded", Reason: "placeholder substituted"})
	}
	if len(substituted) == 0 {
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "forwarded", Reason: "no credential involved"})
	}
	for name, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) transport() *http.Transport {
	p.once.Do(func() {
		dialer := p.Dialer
		if dialer == nil {
			dialer = &net.Dialer{Timeout: 30 * time.Second}
		}
		p.rt = &http.Transport{
			Proxy:                 nil, // never chain through environment proxies
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// addr is the granted host:port (the URL was matched against the
				// grant). Resolve it ourselves rather than trusting anything the
				// sandbox controls as identity.
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupHost(ctx, host)
				if err != nil {
					return nil, fmt.Errorf("resolving %s: %w", host, err)
				}
				if len(ips) == 0 {
					return nil, fmt.Errorf("resolving %s: no addresses", host)
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
			},
		}
	})
	return p.rt
}

type readCloser struct {
	io.Reader
	io.Closer
}
