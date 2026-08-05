// Package proxy is the egress credential proxy (SPEC-git-agent-protocol §9):
// the sandbox never holds a real credential, only a placeholder; the proxy
// substitutes the real value on the way out, and only when the placeholder
// appears in exactly the granted header for the granted destination. A
// placeholder anywhere else is an exfiltration attempt and rejects loudly
// (R9.2). The proxy protects the credential's value, not its capability —
// grants are scoped by method and path prefix because a host-level allowlist
// still permits POST /gists (R9.6/H7).
package proxy

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/flanksource/commons-db/types"
)

// PlaceholderPrefix starts every placeholder the sandbox sees.
const PlaceholderPrefix = "captain-placeholder-"

// HeaderGrant names one header that may carry a credential, and where its
// real value comes from. The value is a types.EnvVar (A4.1): static, or
// resolved from a secret store at request time — the proxy never learns
// anything about storage.
type HeaderGrant struct {
	Name  string       `json:"name" yaml:"name"`
	Value types.EnvVar `json:"valueFrom,omitempty" yaml:"valueFrom,omitempty"`
}

// Grant is one destination the sandbox may reach: host, methods, path
// prefixes, and the headers that may carry credentials to it. Destinations
// are deny-by-default — no grant, no request (R9.3; the network layer must
// additionally block non-HTTP egress, which no proxy can see).
type Grant struct {
	Name    string        `json:"name" yaml:"name"` // stable id; part of the placeholder
	URL     string        `json:"url" yaml:"url"`   // scheme://host[:port]
	Methods []string      `json:"methods,omitempty" yaml:"methods,omitempty"`
	Paths   []string      `json:"paths,omitempty" yaml:"paths,omitempty"`
	Headers []HeaderGrant `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// Placeholder returns the sandbox-visible stand-in for one granted header.
func (g Grant) Placeholder(header string) string {
	return PlaceholderPrefix + g.Name + "-" + strings.ToLower(header)
}

// Validate checks the grant's shape.
func (g Grant) Validate() error {
	if g.Name == "" || strings.ContainsAny(g.Name, " /") {
		return fmt.Errorf("grant name %q must be a bare identifier", g.Name)
	}
	u, err := url.Parse(g.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("grant %s: url %q must be http(s)://host[:port]", g.Name, g.URL)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("grant %s: scope paths belong in paths, not the url", g.Name)
	}
	for _, h := range g.Headers {
		if strings.TrimSpace(h.Name) == "" {
			return fmt.Errorf("grant %s: header grant with no name", g.Name)
		}
	}
	return nil
}

func (g Grant) host() string {
	u, err := url.Parse(g.URL)
	if err != nil {
		return ""
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	return host
}

func (g Grant) rawHost() string {
	u, err := url.Parse(g.URL)
	if err != nil {
		return ""
	}
	return u.Host
}

func (g Grant) scheme() string {
	u, err := url.Parse(g.URL)
	if err != nil {
		return "https"
	}
	return u.Scheme
}

// AllowsMethod reports whether the grant permits the method; an empty list
// permits none — scope is mandatory, not advisory (R9.6).
func (g Grant) AllowsMethod(method string) bool {
	for _, m := range g.Methods {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}

// AllowsPath reports whether the path lies under a granted prefix.
func (g Grant) AllowsPath(path string) bool {
	for _, p := range g.Paths {
		prefix := pathpkgClean(p)
		request := pathpkgClean(path)
		if prefix == "/" || request == prefix || strings.HasPrefix(request, prefix+"/") {
			return true
		}
	}
	return false
}

func pathpkgClean(value string) string {
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return path.Clean(value)
}

// grantedHeader returns the header grant for name, if any.
func (g Grant) grantedHeader(name string) (HeaderGrant, bool) {
	for _, h := range g.Headers {
		if strings.EqualFold(h.Name, name) {
			return h, true
		}
	}
	return HeaderGrant{}, false
}

// SandboxEnv renders the placeholder environment for a set of grants: one
// <GRANT>_<HEADER> entry per granted header, all placeholders, no values.
func SandboxEnv(grants []Grant) []string {
	var env []string
	for _, g := range grants {
		for _, h := range g.Headers {
			key := strings.ToUpper(strings.ReplaceAll(g.Name+"_"+h.Name, "-", "_"))
			env = append(env, key+"="+g.Placeholder(h.Name))
		}
	}
	return env
}

// Resolver turns a header grant's EnvVar into the real value at request
// time. StaticResolver handles inline values; richer resolvers plug in
// secret stores.
type Resolver func(types.EnvVar) (string, error)

// StaticResolver resolves only inline `value:` credentials, failing loudly on
// anything it cannot resolve — the placeholder is never forwarded (R9.4).
func StaticResolver(v types.EnvVar) (string, error) {
	if v.ValueStatic != "" {
		return v.ValueStatic, nil
	}
	return "", fmt.Errorf("credential is not statically resolvable (valueFrom requires a store-backed resolver)")
}
