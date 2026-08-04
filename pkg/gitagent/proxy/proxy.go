package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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
		http.Error(w, "captain-proxy: absolute-form proxy requests only", http.StatusBadRequest)
		return
	}
	grant, ok := p.grantFor(r.URL.Hostname(), r.URL.Port(), r.URL.Scheme)
	if !ok {
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "rejected", Reason: "destination not granted (deny by default)"})
		http.Error(w, "captain-proxy: destination not granted", http.StatusForbidden)
		return
	}
	if reason := p.findMisplacedPlaceholder(r, grant); reason != "" {
		// Rejected and logged, never stripped-and-forwarded: the appearance
		// is an exfiltration attempt and silently continuing hides it (R9.2).
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "rejected", Reason: reason})
		http.Error(w, "captain-proxy: "+reason, http.StatusForbidden)
		return
	}
	if !grant.AllowsMethod(r.Method) || !grant.AllowsPath(r.URL.Path) {
		p.audit(Decision{Method: r.Method, Destination: r.URL.Host, Verdict: "rejected", Reason: "method or path outside the grant's scope (R9.6)"})
		http.Error(w, "captain-proxy: method or path outside the grant's scope", http.StatusForbidden)
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

// grantFor matches a destination against the grant table.
func (p *Proxy) grantFor(host, port, scheme string) (Grant, bool) {
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	for _, g := range p.Grants {
		if g.host() == net.JoinHostPort(host, port) && g.scheme() == scheme {
			return g, true
		}
	}
	return Grant{}, false
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
		prefix := make([]byte, maxScannedBody)
		n, _ := io.ReadFull(r.Body, prefix)
		body := prefix[:n]
		rest := r.Body
		r.Body = readCloser{io.MultiReader(strings.NewReader(string(body)), rest), rest}
		if strings.Contains(string(body), PlaceholderPrefix) {
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
	dialer := p.Dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 30 * time.Second}
	}
	transport := &http.Transport{
		Proxy: nil, // never chain through environment proxies
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// addr is the granted host:port (the URL was matched against the
			// grant). Resolve it ourselves rather than trusting anything the
			// sandbox controls as identity.
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("resolving %s: %w", host, err)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
		},
	}
	defer transport.CloseIdleConnections()

	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL.Scheme = grant.scheme()
	out.URL.Host = strings.TrimSuffix(strings.TrimSuffix(grant.host(), ":443"), ":80")
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

type readCloser struct {
	io.Reader
	io.Closer
}
