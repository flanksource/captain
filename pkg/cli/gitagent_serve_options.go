package cli

import (
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

type GitAgentServeOptions struct {
	Backend         string               `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
	Listen          string               `flag:"listen" help:"Address to serve git-receive-pack on" default:":7422"`
	Root            string               `flag:"root" help:"Directory of receivable repos (default <keys-dir>/repos)"`
	Role            string               `flag:"role" help:"Receiver role: sidecar (runs beside a coding agent) or mailbox (the supervisor's receiver)" default:"sidecar"`
	Transport       string               `flag:"transport" help:"sidecar role: protocol for the receive endpoint. ssh authenticates the supervisor by key; https terminates TLS here and authenticates it by a bearer token this agent issues, and needs --advertise https://host/git/repo.git" default:"ssh"`
	Advertise       string               `flag:"advertise" help:"sidecar role: ssh:// or https:// endpoint the supervisor should dispatch to (default: the address the supervisor sees)"`
	Token           text.SensitiveString `flag:"token" help:"Captain token printed by 'captain sandbox git-agent add'"`
	TokenFile       string               `flag:"token-file" help:"File holding the captain token. Use this rather than --token for a workload, where argv is world-readable"`
	Supervisor      string               `flag:"supervisor" help:"ssh:// or https:// endpoint of the supervisor to enroll with"`
	HostFingerprint string               `flag:"host-fingerprint" help:"Supervisor identity to pin, printed by 'git-agent add': its SSH host key for ssh://, its TLS public-key pin for https://"`
	TLSCert         string               `flag:"tls-cert" help:"HTTPS sidecar certificate file; requires --tls-key"`
	TLSKey          string               `flag:"tls-key" help:"HTTPS sidecar private-key file; requires --tls-cert"`
}

func (o GitAgentServeOptions) enrollmentToken() (text.SensitiveString, error) {
	inline := strings.TrimSpace(o.Token.Value())
	path := strings.TrimSpace(o.TokenFile)
	switch {
	case inline != "" && path != "":
		return "", fmt.Errorf("--token and --token-file are mutually exclusive")
	case inline != "":
		return text.NewSensitiveString(inline), nil
	case path == "":
		return "", nil
	}
	token, err := gitagent.ReadTokenFile(path)
	if err != nil {
		return "", fmt.Errorf("--token-file: %w", err)
	}
	return token, nil
}

func reusableEnrollment(
	opts GitAgentServeOptions, transport mailboxTransport, token text.SensitiveString, keysDir string,
) (string, error) {
	cfg, exists, err := captainconfig.Load()
	if err != nil {
		return "", fmt.Errorf("load persisted enrollment: %w", err)
	}
	backend, ok := cfg.Sandbox.Backends[opts.Backend]
	if !exists || !ok {
		return "", nil
	}
	supervisor, ok := backend.Options["supervisor"].(map[string]any)
	agent, _ := supervisor["agent"].(string)
	url, _ := supervisor["url"].(string)
	fingerprint, _ := supervisor["hostFingerprint"].(string)
	if !ok || strings.TrimSpace(agent) == "" || strings.TrimSuffix(url, "/") != strings.TrimSuffix(opts.Supervisor, "/") ||
		strings.TrimSpace(fingerprint) != strings.TrimSpace(opts.HostFingerprint) {
		return "", nil
	}
	tokenPath, _ := supervisor["tokenPath"].(string)
	storedToken, err := gitagent.ReadTokenFile(tokenPath)
	if err != nil || subtle.ConstantTimeCompare([]byte(storedToken.Value()), []byte(token.Value())) != 1 {
		return "", nil
	}
	if transport == transportHTTPS {
		if _, err := gitagent.LoadDispatchCredential(filepath.Join(keysDir, gitagent.DispatchCredentialName)); err != nil {
			return "", nil
		}
	} else {
		agents, _ := backend.Options["agents"].(map[string]any)
		dispatch, _ := agents[supervisorAgentID].(map[string]any)
		if fingerprint, _ := dispatch["fingerprint"].(string); strings.TrimSpace(fingerprint) == "" {
			return "", nil
		}
		if _, err := os.Stat(filepath.Join(keysDir, agentKeyName)); err != nil {
			return "", nil
		}
	}
	return strings.TrimSpace(agent), nil
}
