package api

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

type SandboxFilesystemAccess string

const (
	SandboxFilesystemReadOnly       SandboxFilesystemAccess = "read-only"
	SandboxFilesystemWorkspaceWrite SandboxFilesystemAccess = "workspace-write"
)

func (a SandboxFilesystemAccess) Valid() bool {
	return a == "" || a == SandboxFilesystemReadOnly || a == SandboxFilesystemWorkspaceWrite
}

type SandboxNetworkAccess string

const (
	SandboxNetworkDisabled     SandboxNetworkAccess = "disabled"
	SandboxNetworkRestricted   SandboxNetworkAccess = "restricted"
	SandboxNetworkUnrestricted SandboxNetworkAccess = "unrestricted"
)

func (a SandboxNetworkAccess) Valid() bool {
	return a == "" || a == SandboxNetworkDisabled || a == SandboxNetworkRestricted || a == SandboxNetworkUnrestricted
}

// NativeSandboxPolicy is Captain's provider-neutral filesystem/network policy.
// Translators publish which fields they can honour and reject every configured
// field they cannot express; native provider settings never enter a Spec.
type NativeSandboxPolicy struct {
	Required    *bool                     `json:"required,omitempty" yaml:"required,omitempty"`
	Filesystem  *SandboxFilesystemPolicy  `json:"filesystem,omitempty" yaml:"filesystem,omitempty"`
	Network     *SandboxNetworkPolicy     `json:"network,omitempty" yaml:"network,omitempty"`
	Commands    *SandboxCommandPolicy     `json:"commands,omitempty" yaml:"commands,omitempty"`
	Credentials *SandboxCredentialsPolicy `json:"credentials,omitempty" yaml:"credentials,omitempty"`
	Platform    *SandboxPlatformPolicy    `json:"platform,omitempty" yaml:"platform,omitempty"`
}

type SandboxFilesystemPolicy struct {
	Access            SandboxFilesystemAccess `json:"access,omitempty" yaml:"access,omitempty"`
	WritableRoots     []string                `json:"writableRoots,omitempty" yaml:"writableRoots,omitempty"`
	ReadableRoots     []string                `json:"readableRoots,omitempty" yaml:"readableRoots,omitempty"`
	DeniedReadRoots   []string                `json:"deniedReadRoots,omitempty" yaml:"deniedReadRoots,omitempty"`
	DeniedWriteRoots  []string                `json:"deniedWriteRoots,omitempty" yaml:"deniedWriteRoots,omitempty"`
	IncludeSystemTemp *bool                   `json:"includeSystemTemp,omitempty" yaml:"includeSystemTemp,omitempty"`
}

type SandboxNetworkPolicy struct {
	Access              SandboxNetworkAccess `json:"access,omitempty" yaml:"access,omitempty"`
	AllowedDomains      []string             `json:"allowedDomains,omitempty" yaml:"allowedDomains,omitempty"`
	DeniedDomains       []string             `json:"deniedDomains,omitempty" yaml:"deniedDomains,omitempty"`
	AllowedUnixSockets  []string             `json:"allowedUnixSockets,omitempty" yaml:"allowedUnixSockets,omitempty"`
	AllowAllUnixSockets *bool                `json:"allowAllUnixSockets,omitempty" yaml:"allowAllUnixSockets,omitempty"`
	AllowLocalBinding   *bool                `json:"allowLocalBinding,omitempty" yaml:"allowLocalBinding,omitempty"`
	AllowedMachServices []string             `json:"allowedMachServices,omitempty" yaml:"allowedMachServices,omitempty"`
	HTTPProxyPort       *int                 `json:"httpProxyPort,omitempty" yaml:"httpProxyPort,omitempty"`
	SOCKSProxyPort      *int                 `json:"socksProxyPort,omitempty" yaml:"socksProxyPort,omitempty"`
}

type SandboxCommandPolicy struct {
	ExcludedFromSandbox []string `json:"excludedFromSandbox,omitempty" yaml:"excludedFromSandbox,omitempty"`
	AllowUnsandboxed    *bool    `json:"allowUnsandboxed,omitempty" yaml:"allowUnsandboxed,omitempty"`
}

type SandboxCredentialsPolicy struct {
	DeniedFiles []string `json:"deniedFiles,omitempty" yaml:"deniedFiles,omitempty"`
	DeniedEnv   []string `json:"deniedEnv,omitempty" yaml:"deniedEnv,omitempty"`
	MaskedEnv   []string `json:"maskedEnv,omitempty" yaml:"maskedEnv,omitempty"`
}

type SandboxPlatformPolicy struct {
	AllowAppleEvents       *bool `json:"allowAppleEvents,omitempty" yaml:"allowAppleEvents,omitempty"`
	WeakerNestedIsolation  *bool `json:"weakerNestedIsolation,omitempty" yaml:"weakerNestedIsolation,omitempty"`
	WeakerNetworkIsolation *bool `json:"weakerNetworkIsolation,omitempty" yaml:"weakerNetworkIsolation,omitempty"`
}

// SandboxDispatchPolicy bounds Git Agent submission and retry behavior.
type SandboxDispatchPolicy struct {
	Paths       []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	MaxAttempts int      `json:"maxAttempts,omitempty" yaml:"maxAttempts,omitempty"`
}

func (SandboxFilesystemAccess) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        enumValues([]SandboxFilesystemAccess{SandboxFilesystemReadOnly, SandboxFilesystemWorkspaceWrite}),
		Description: "Native filesystem access granted to built-in provider tools",
	}
}

func (SandboxNetworkAccess) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "string",
		Enum: enumValues([]SandboxNetworkAccess{
			SandboxNetworkDisabled,
			SandboxNetworkRestricted,
			SandboxNetworkUnrestricted,
		}),
		Description: "Native network access granted to built-in provider tools",
	}
}

func nativeSandboxPolicySchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("required", booleanSchema("Fail when the provider-native sandbox is unavailable"))
	properties.Set("filesystem", sandboxFilesystemPolicySchema())
	properties.Set("network", sandboxNetworkPolicySchema())
	properties.Set("commands", sandboxCommandPolicySchema())
	properties.Set("credentials", sandboxCredentialsPolicySchema())
	properties.Set("platform", sandboxPlatformPolicySchema())
	return objectSchema("Provider-neutral native sandbox policy", properties)
}

func sandboxFilesystemPolicySchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("access", SandboxFilesystemAccess("").JSONSchema())
	properties.Set("writableRoots", stringArraySchema("Additional writable roots"))
	properties.Set("readableRoots", stringArraySchema("Additional readable roots"))
	properties.Set("deniedReadRoots", stringArraySchema("Roots built-in tools cannot read"))
	properties.Set("deniedWriteRoots", stringArraySchema("Roots built-in tools cannot write"))
	properties.Set("includeSystemTemp", booleanSchema("Include system temporary directories in writable roots"))
	return objectSchema("Filesystem isolation", properties)
}

func sandboxNetworkPolicySchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("access", SandboxNetworkAccess("").JSONSchema())
	properties.Set("allowedDomains", stringArraySchema("Allowed outbound domains"))
	properties.Set("deniedDomains", stringArraySchema("Denied outbound domains"))
	properties.Set("allowedUnixSockets", stringArraySchema("Allowed Unix-domain socket paths"))
	properties.Set("allowAllUnixSockets", booleanSchema("Allow every Unix-domain socket"))
	properties.Set("allowLocalBinding", booleanSchema("Allow binding local listening ports"))
	properties.Set("allowedMachServices", stringArraySchema("Allowed macOS Mach services"))
	properties.Set("httpProxyPort", portSchema("HTTP proxy port"))
	properties.Set("socksProxyPort", portSchema("SOCKS proxy port"))
	return objectSchema("Network isolation", properties)
}

func sandboxCommandPolicySchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("excludedFromSandbox", stringArraySchema("Commands that run outside native process isolation"))
	properties.Set("allowUnsandboxed", booleanSchema("Allow a provider to request unsandboxed command execution"))
	return objectSchema("Command isolation", properties)
}

func sandboxCredentialsPolicySchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("deniedFiles", stringArraySchema("Credential files hidden from built-in tools"))
	properties.Set("deniedEnv", stringArraySchema("Environment variables removed from built-in tools"))
	properties.Set("maskedEnv", stringArraySchema("Environment variables masked in output"))
	return objectSchema("Credential isolation", properties)
}

func sandboxPlatformPolicySchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("allowAppleEvents", booleanSchema("Allow macOS Apple Events"))
	properties.Set("weakerNestedIsolation", booleanSchema("Permit weaker isolation in nested environments"))
	properties.Set("weakerNetworkIsolation", booleanSchema("Permit weaker network isolation when proxy enforcement is unavailable"))
	return objectSchema("Platform-specific isolation", properties)
}

func sandboxDispatchPolicySchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("paths", stringArraySchema("Gitignore-style path policy for submitted changes"))
	properties.Set("maxAttempts", &jsonschema.Schema{
		Type:        "integer",
		Minimum:     json.Number("0"),
		Description: "Maximum Git Agent submit cycles",
	})
	return objectSchema("Git Agent dispatch policy", properties)
}

func objectSchema(description string, properties *orderedmap.OrderedMap[string, *jsonschema.Schema]) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		Description:          description,
		Properties:           properties,
		AdditionalProperties: jsonschema.FalseSchema,
	}
}

func booleanSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "boolean", Description: description}
}

func stringArraySchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"}, Description: description}
}

func portSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "integer",
		Minimum:     json.Number("1"),
		Maximum:     json.Number("65535"),
		Description: description,
	}
}

func (p *NativeSandboxPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if p.Filesystem != nil && !p.Filesystem.Access.Valid() {
		return fmt.Errorf("sandbox policy filesystem.access %q is invalid", p.Filesystem.Access)
	}
	if p.Network != nil {
		if !p.Network.Access.Valid() {
			return fmt.Errorf("sandbox policy network.access %q is invalid", p.Network.Access)
		}
		for name, port := range map[string]*int{"httpProxyPort": p.Network.HTTPProxyPort, "socksProxyPort": p.Network.SOCKSProxyPort} {
			if port != nil && (*port < 1 || *port > 65535) {
				return fmt.Errorf("sandbox policy network.%s must be between 1 and 65535", name)
			}
		}
	}
	return nil
}

func (p *SandboxDispatchPolicy) Validate() error {
	if p != nil && p.MaxAttempts < 0 {
		return fmt.Errorf("sandbox dispatch maxAttempts must be >= 0, got %d", p.MaxAttempts)
	}
	return nil
}
