package sandbox

import (
	"fmt"
	"strings"

	sandboxruntime "github.com/flanksource/sandbox-runtime/sandbox"
)

// The sub-config types are aliases for sandbox-runtime's public types — one
// definition, upstream's, following upstream's own convention (its Config is
// itself an alias for the internal SandboxRuntimeConfig). The previous
// hand-copied definitions drifted silently from the module actually enforcing
// them.
type (
	MitmProxyConfig  = sandboxruntime.MitmProxyConfig
	NetworkConfig    = sandboxruntime.NetworkConfig
	FilesystemConfig = sandboxruntime.FilesystemConfig
	RipgrepConfig    = sandboxruntime.RipgrepConfig
	SeccompConfig    = sandboxruntime.SeccompConfig
)

// SandboxRuntimeConfig cannot yet be an alias for sandboxruntime.Config: its
// Tokens field must be captain's *TokensConfig (the token-acquisition machinery
// lives here), and upstream keeps its token types in an internal package. Once
// upstream exports them, this struct collapses into an alias too — that fix
// belongs upstream, not as a fork here.
type SandboxRuntimeConfig struct {
	Network                      NetworkConfig       `json:"network" yaml:"network"`
	Filesystem                   FilesystemConfig    `json:"filesystem" yaml:"filesystem"`
	Env                          map[string]string   `json:"env,omitempty" yaml:"env,omitempty"`
	PassthroughEnv               []string            `json:"passthroughEnv,omitempty" yaml:"passthroughEnv,omitempty"`
	IgnoreViolations             map[string][]string `json:"ignoreViolations,omitempty" yaml:"ignoreViolations,omitempty"`
	EnableWeakerNestedSandbox    bool                `json:"enableWeakerNestedSandbox,omitempty" yaml:"enableWeakerNestedSandbox,omitempty"`
	EnableWeakerNetworkIsolation bool                `json:"enableWeakerNetworkIsolation,omitempty" yaml:"enableWeakerNetworkIsolation,omitempty"`
	Ripgrep                      *RipgrepConfig      `json:"ripgrep,omitempty" yaml:"ripgrep,omitempty"`
	MandatoryDenySearchDepth     int                 `json:"mandatoryDenySearchDepth,omitempty" yaml:"mandatoryDenySearchDepth,omitempty"`
	AllowPty                     bool                `json:"allowPty,omitempty" yaml:"allowPty,omitempty"`
	Seccomp                      *SeccompConfig      `json:"seccomp,omitempty" yaml:"seccomp,omitempty"`
	Tokens                       *TokensConfig       `json:"tokens,omitempty" yaml:"tokens,omitempty"`
}

func DefaultConfig() SandboxRuntimeConfig {
	return SandboxRuntimeConfig{
		Network: NetworkConfig{
			AllowedDomains: []string{},
			DeniedDomains:  []string{},
		},
		Filesystem: FilesystemConfig{
			DenyRead:   []string{},
			AllowWrite: []string{},
			DenyWrite:  []string{},
		},
	}
}

func (c *SandboxRuntimeConfig) MergeFrom(other *SandboxRuntimeConfig) {
	c.Network.AllowedDomains = mergeStringSlicesDedup(c.Network.AllowedDomains, other.Network.AllowedDomains)
	c.Network.DeniedDomains = mergeStringSlicesDedup(c.Network.DeniedDomains, other.Network.DeniedDomains)
	c.Network.AllowUnixSockets = mergeStringSlicesDedup(c.Network.AllowUnixSockets, other.Network.AllowUnixSockets)
	c.Network.AllowAllUnixSockets = c.Network.AllowAllUnixSockets || other.Network.AllowAllUnixSockets
	c.Network.AllowLocalBinding = c.Network.AllowLocalBinding || other.Network.AllowLocalBinding
	if other.Network.HTTPProxyPort != nil {
		c.Network.HTTPProxyPort = other.Network.HTTPProxyPort
	}
	if other.Network.SocksProxyPort != nil {
		c.Network.SocksProxyPort = other.Network.SocksProxyPort
	}
	if other.Network.MitmProxy != nil {
		c.Network.MitmProxy = other.Network.MitmProxy
	}
	c.Filesystem.DenyRead = mergeStringSlicesDedup(c.Filesystem.DenyRead, other.Filesystem.DenyRead)
	c.Filesystem.AllowWrite = mergeStringSlicesDedup(c.Filesystem.AllowWrite, other.Filesystem.AllowWrite)
	c.Filesystem.DenyWrite = mergeStringSlicesDedup(c.Filesystem.DenyWrite, other.Filesystem.DenyWrite)
	c.Filesystem.AllowGitConfig = c.Filesystem.AllowGitConfig || other.Filesystem.AllowGitConfig
	c.PassthroughEnv = mergeStringSlicesDedup(c.PassthroughEnv, other.PassthroughEnv)
	c.EnableWeakerNestedSandbox = c.EnableWeakerNestedSandbox || other.EnableWeakerNestedSandbox
	c.EnableWeakerNetworkIsolation = c.EnableWeakerNetworkIsolation || other.EnableWeakerNetworkIsolation
	c.AllowPty = c.AllowPty || other.AllowPty
	if other.Env != nil {
		if c.Env == nil {
			c.Env = make(map[string]string)
		}
		for k, v := range other.Env {
			c.Env[k] = v
		}
	}
	if other.IgnoreViolations != nil {
		if c.IgnoreViolations == nil {
			c.IgnoreViolations = make(map[string][]string)
		}
		for k, v := range other.IgnoreViolations {
			c.IgnoreViolations[k] = mergeStringSlicesDedup(c.IgnoreViolations[k], v)
		}
	}
	c.Tokens = MergeTokensConfig(c.Tokens, other.Tokens)
}

func mergeStringSlicesDedup(s1, s2 []string) []string {
	seen := make(map[string]struct{}, len(s1)+len(s2))
	result := make([]string, 0, len(s1)+len(s2))

	for _, value := range s1 {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	for _, value := range s2 {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

func (c *SandboxRuntimeConfig) NormalizeAndValidate() error {
	for _, d := range c.Network.AllowedDomains {
		if !isValidDomainPattern(d) {
			return fmt.Errorf("invalid allowed domain pattern: %q", d)
		}
	}
	for _, d := range c.Network.DeniedDomains {
		if !isValidDomainPattern(d) {
			return fmt.Errorf("invalid denied domain pattern: %q", d)
		}
	}

	if c.Network.HTTPProxyPort != nil {
		if *c.Network.HTTPProxyPort < 1 || *c.Network.HTTPProxyPort > 65535 {
			return fmt.Errorf("httpProxyPort out of range: %d", *c.Network.HTTPProxyPort)
		}
	}
	if c.Network.SocksProxyPort != nil {
		if *c.Network.SocksProxyPort < 1 || *c.Network.SocksProxyPort > 65535 {
			return fmt.Errorf("socksProxyPort out of range: %d", *c.Network.SocksProxyPort)
		}
	}

	if c.Network.MitmProxy != nil {
		if strings.TrimSpace(c.Network.MitmProxy.SocketPath) == "" {
			return fmt.Errorf("mitmProxy.socketPath cannot be empty")
		}
		if len(c.Network.MitmProxy.Domains) == 0 {
			return fmt.Errorf("mitmProxy.domains cannot be empty")
		}
		for _, d := range c.Network.MitmProxy.Domains {
			if !isValidDomainPattern(d) {
				return fmt.Errorf("invalid mitmProxy domain pattern: %q", d)
			}
		}
	}

	if c.MandatoryDenySearchDepth != 0 {
		if c.MandatoryDenySearchDepth < 1 || c.MandatoryDenySearchDepth > 10 {
			return fmt.Errorf("mandatoryDenySearchDepth must be between 1 and 10")
		}
	}

	if c.Ripgrep != nil {
		if strings.TrimSpace(c.Ripgrep.Command) == "" {
			return fmt.Errorf("ripgrep.command cannot be empty")
		}
	}

	for _, p := range c.Filesystem.DenyRead {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("filesystem.denyRead contains empty path")
		}
	}
	for _, p := range c.Filesystem.AllowWrite {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("filesystem.allowWrite contains empty path")
		}
	}
	for _, p := range c.Filesystem.DenyWrite {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("filesystem.denyWrite contains empty path")
		}
	}

	return nil
}

func isValidDomainPattern(val string) bool {
	if strings.Contains(val, "://") || strings.Contains(val, "/") || strings.Contains(val, ":") {
		return false
	}
	if val == "localhost" {
		return true
	}

	if strings.HasPrefix(val, "*.") {
		domain := strings.TrimPrefix(val, "*.")
		if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
			return false
		}
		parts := strings.Split(domain, ".")
		if len(parts) < 2 {
			return false
		}
		for _, p := range parts {
			if p == "" {
				return false
			}
		}
		return true
	}

	if strings.Contains(val, "*") {
		return false
	}

	return strings.Contains(val, ".") && !strings.HasPrefix(val, ".") && !strings.HasSuffix(val, ".")
}
