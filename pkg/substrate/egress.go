// What an environment may reach: hosts whose TLS is passed through, hosts reached in cleartext,
// and hosts whose requests the egress gateway puts a credential into. Everything else is denied.
package substrate

import (
	"errors"
	"fmt"
	"net"

	"github.com/flanksource/captain/pkg/substrate/internal/ateapipb"
)

// HostPort is a host, by DNS name, and a port on it.
type HostPort struct {
	Host string
	Port int
}

// Credential is a credential the egress gateway puts into requests to Host over HTTPS: it
// replaces Header's value with Prefix and the credential URI names, which the cluster's
// credential provider resolves, such as ate-secret://k8s.io/default/<namespace>/<secret>/<key>. Env is the
// variable a process's client reads the credential from; the process gets a Placeholder in it.
type Credential struct {
	Host   string
	Header string
	Prefix string
	URI    string
	Env    string
}

// Egress is what an environment may reach.
type Egress struct {
	// Passthrough are hosts reached over TLS the gateway passes through undecrypted.
	Passthrough []HostPort
	// HTTP are hosts reached in cleartext.
	HTTP []HostPort
	// Credentials are hosts reached over HTTPS with a credential the gateway injects.
	Credentials []Credential
}

// egressPolicy is the policy that allows egress, in atespace. Hosts sharing a port share a rule.
func egressPolicy(atespace string, egress Egress) (*ateapipb.EgressPolicy, error) {
	passthrough, err := byPort(egress.Passthrough)
	if err != nil {
		return nil, err
	}
	cleartext, err := byPort(egress.HTTP)
	if err != nil {
		return nil, err
	}
	rules := []*ateapipb.EgressRule{}
	for _, group := range passthrough {
		rules = append(rules, &ateapipb.EgressRule{TlsPassthrough: &ateapipb.TlsPassthroughRule{Hostnames: group.hosts, Ports: group.ports()}})
	}
	for _, group := range cleartext {
		rules = append(rules, &ateapipb.EgressRule{Http: &ateapipb.HttpRule{Hostnames: group.hosts, Ports: group.ports()}})
	}
	for _, credential := range egress.Credentials {
		if credential.Host == "" || credential.Header == "" || credential.URI == "" {
			return nil, fmt.Errorf("the credential for %s needs its host, header and URI", credential.Host)
		}
		if err := dnsName(credential.Host); err != nil {
			return nil, err
		}
		rules = append(rules, &ateapipb.EgressRule{Https: &ateapipb.HttpsRule{
			Hostnames: []string{credential.Host},
			Effects: &ateapipb.HttpRuleEffects{ReplaceHeaders: []*ateapipb.CredentialHeader{{
				Header: credential.Header, Prefix: credential.Prefix, CredentialUri: credential.URI,
			}}},
		}})
	}
	return &ateapipb.EgressPolicy{
		Metadata: &ateapipb.ResourceMetadata{Atespace: atespace, Name: "default"},
		Rules:    rules,
	}, nil
}

type portGroup struct {
	port  int
	hosts []string
}

func (g portGroup) ports() *ateapipb.Ports {
	return &ateapipb.Ports{Numbers: []int32{int32(g.port)}} //nolint:gosec // byPort checked it is at most 65535
}

// byPort groups hosts by port, in the order each port first appears.
func byPort(hosts []HostPort) ([]portGroup, error) {
	var groups []portGroup
	for _, hostPort := range hosts {
		if hostPort.Host == "" {
			return nil, errors.New("substrate egress names a port with no host")
		}
		if err := dnsName(hostPort.Host); err != nil {
			return nil, err
		}
		if hostPort.Port < 1 || hostPort.Port > 65535 {
			return nil, fmt.Errorf("substrate egress to %s: port %d is outside 1-65535", hostPort.Host, hostPort.Port)
		}
		i := 0
		for i < len(groups) && groups[i].port != hostPort.Port {
			i++
		}
		if i == len(groups) {
			groups = append(groups, portGroup{port: hostPort.Port})
		}
		groups[i].hosts = append(groups[i].hosts, hostPort.Host)
	}
	return groups, nil
}

// dnsName refuses an IP address: substrate's egress rules match hosts by name.
func dnsName(host string) error {
	if net.ParseIP(host) != nil {
		return fmt.Errorf("substrate egress names hosts by DNS name, not IP address: %q", host)
	}
	return nil
}
