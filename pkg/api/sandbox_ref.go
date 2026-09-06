package api

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// SandboxRef selects one of Captain's four sandbox modes. A scalar is shorthand
// for a mode with no overrides; the object form carries the provider-neutral
// native-isolation policy plus external-backend selection.
//
// A sandbox bounds what the process can reach. Whether the agent asks before
// acting is Permissions.Mode, which is deliberately not represented here: the
// two are independent, and a run with the sandbox off may still want a
// restrictive posture.
type SandboxRef struct {
	Mode SandboxKind `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Backend names a configured Docker or Git Agent backend.
	Backend string `json:"backend,omitempty" yaml:"backend,omitempty"`
	// Policy is translated into the active provider's native sandbox settings.
	Policy *NativeSandboxPolicy `json:"policy,omitempty" yaml:"policy,omitempty"`
	// Agent optionally pins one enrolled agent of a Git Agent backend.
	Agent string `json:"agent,omitempty" yaml:"agent,omitempty"`
	// Dispatch bounds Git Agent submissions and retries.
	Dispatch *SandboxDispatchPolicy `json:"dispatch,omitempty" yaml:"dispatch,omitempty"`
}

type sandboxRefAlias SandboxRef

func (r SandboxRef) isScalar() bool {
	return r.Backend == "" && r.Policy == nil && r.Agent == "" && r.Dispatch == nil
}

func (r SandboxRef) MarshalJSON() ([]byte, error) {
	if r.isScalar() {
		return json.Marshal(r.Mode)
	}
	return json.Marshal(sandboxRefAlias(r))
}

func (r *SandboxRef) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		return nil
	}
	var scalar string
	if err := json.Unmarshal(data, &scalar); err == nil {
		*r = SandboxRef{Mode: SandboxKind(scalar)}
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var alias sandboxRefAlias
	if err := decoder.Decode(&alias); err != nil {
		return err
	}
	*r = SandboxRef(alias)
	return nil
}

func (r SandboxRef) MarshalYAML() (any, error) {
	if r.isScalar() {
		return r.Mode, nil
	}
	return sandboxRefAlias(r), nil
}

func (r *SandboxRef) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var scalar string
		if err := value.Decode(&scalar); err != nil {
			return err
		}
		*r = SandboxRef{Mode: SandboxKind(scalar)}
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("sandbox must be a mode or a mapping, got %s", value.Tag)
	}
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode sandbox mapping: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	var alias sandboxRefAlias
	if err := decoder.Decode(&alias); err != nil {
		return err
	}
	*r = SandboxRef(alias)
	return nil
}

func (SandboxRef) JSONSchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("mode", sandboxModeSchema())
	properties.Set("backend", &jsonschema.Schema{
		Type:        "string",
		Description: "Configured Docker or Git Agent backend",
	})
	properties.Set("policy", nativeSandboxPolicySchema())
	properties.Set("agent", &jsonschema.Schema{
		Type:        "string",
		Description: "Pin one enrolled Git Agent worker",
	})
	properties.Set("dispatch", sandboxDispatchPolicySchema())

	return &jsonschema.Schema{
		Description: "Unified sandbox mode and provider-neutral isolation policy",
		OneOf: []*jsonschema.Schema{
			sandboxModeSchema(),
			{
				Type:                 "object",
				Properties:           properties,
				AnyOf:                []*jsonschema.Schema{{Required: []string{"mode"}}, {Required: []string{"backend"}}},
				AdditionalProperties: jsonschema.FalseSchema,
			},
		},
	}
}

func sandboxModeSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        enumValues(AllSandboxModes()),
		Description: "Isolation boundary: off, provider-native, Docker, or Git Agent",
	}
}

func (r SandboxRef) Validate() error {
	if err := r.Mode.Validate(); err != nil {
		return err
	}
	return r.ValidateStructure()
}

// ValidateStructure permits a named backend pending captured-context selection.
func (r SandboxRef) ValidateStructure() error {
	if r.Mode != "" || r.Backend == "" {
		if err := r.Mode.Validate(); err != nil {
			return err
		}
	}
	if r.Policy != nil && r.Mode != SandboxNative {
		return fmt.Errorf("native policy requires sandbox mode native, got %q", r.Mode)
	}
	if r.Backend != "" && r.Mode != "" && r.Mode != SandboxDocker && r.Mode != SandboxGitAgent {
		return fmt.Errorf("sandbox backend is only valid for docker or git-agent mode, got %q", r.Mode)
	}
	if (r.Agent != "" || r.Dispatch != nil) && r.Mode != "" && r.Mode != SandboxGitAgent {
		return fmt.Errorf("sandbox agent/dispatch settings require git-agent mode, got %q", r.Mode)
	}
	if err := r.Policy.Validate(); err != nil {
		return err
	}
	return r.Dispatch.Validate()
}
