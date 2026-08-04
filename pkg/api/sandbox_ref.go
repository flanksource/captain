package api

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// SandboxRef selects the sandbox for one run. It accepts two forms, following
// the Permissions.Tools precedent:
//
//	sandbox: git-agent            # scalar: a kind or a configured backend name
//
//	sandbox:                      # object: backend plus overrides
//	  backend: prod-pool
//	  agent: worker-01
//	  policy: {paths: ["pkg/**"], maxAttempts: 3}
//
// The scalar form is sugar for {backend: <value>}. Whether the name is a bare
// adapter kind or a configured backend is resolved at the config layer, which
// knows the configured names; this type only carries the reference.
type SandboxRef struct {
	// Backend names a configured sandbox backend from ~/.captain.yaml, or a bare
	// adapter kind (none, srt, container, git-agent).
	Backend string `json:"backend,omitempty" yaml:"backend,omitempty"`
	// Agent optionally pins one enrolled agent of a git-agent backend.
	Agent string `json:"agent,omitempty" yaml:"agent,omitempty"`
	// Policy optionally overrides the backend's dispatch policy for this run.
	Policy *SandboxPolicy `json:"policy,omitempty" yaml:"policy,omitempty"`
}

// SandboxPolicy bounds what a dispatched run may touch and how often it may
// retry. Zero values inherit the backend's configured policy.
type SandboxPolicy struct {
	// Paths is the path allow/deny list, gitignore syntax with ! negating.
	Paths []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	// MaxAttempts bounds submit cycles per task.
	MaxAttempts int `json:"maxAttempts,omitempty" yaml:"maxAttempts,omitempty"`
}

// sandboxRefAlias breaks marshal recursion: it has SandboxRef's fields but none
// of its methods.
type sandboxRefAlias SandboxRef

// isScalar reports whether the ref carries only a backend name and so can
// round-trip through the scalar form.
func (r SandboxRef) isScalar() bool {
	return r.Agent == "" && r.Policy == nil
}

func (r SandboxRef) MarshalJSON() ([]byte, error) {
	if r.isScalar() {
		return json.Marshal(r.Backend)
	}
	return json.Marshal(sandboxRefAlias(r))
}

func (r *SandboxRef) UnmarshalJSON(data []byte) error {
	var scalar string
	if err := json.Unmarshal(data, &scalar); err == nil {
		*r = SandboxRef{Backend: scalar}
		return nil
	}
	var alias sandboxRefAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*r = SandboxRef(alias)
	return nil
}

func (r SandboxRef) MarshalYAML() (any, error) {
	if r.isScalar() {
		return r.Backend, nil
	}
	return sandboxRefAlias(r), nil
}

func (r *SandboxRef) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var scalar string
		if err := value.Decode(&scalar); err != nil {
			return err
		}
		*r = SandboxRef{Backend: scalar}
		return nil
	}
	// Decode key-by-key rather than through yaml.Node.Decode: Decode spins up a
	// NON-strict decoder, silently dropping the outer KnownFields(true) — a
	// typo'd key ("backed:") would otherwise yield an empty ref and a run with
	// no sandbox at all.
	*r = SandboxRef{}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("sandbox must be a backend name or a mapping, got %s", value.Tag)
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key, val := value.Content[i].Value, value.Content[i+1]
		switch key {
		case "backend":
			if err := val.Decode(&r.Backend); err != nil {
				return err
			}
		case "agent":
			if err := val.Decode(&r.Agent); err != nil {
				return err
			}
		case "policy":
			r.Policy = &SandboxPolicy{}
			if err := r.Policy.unmarshalStrict(val); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown sandbox key %q (valid: backend, agent, policy)", key)
		}
	}
	return nil
}

func (p *SandboxPolicy) unmarshalStrict(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("sandbox policy must be a mapping, got %s", value.Tag)
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key, val := value.Content[i].Value, value.Content[i+1]
		switch key {
		case "paths":
			if err := val.Decode(&p.Paths); err != nil {
				return err
			}
		case "maxAttempts":
			if err := val.Decode(&p.MaxAttempts); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown sandbox policy key %q (valid: paths, maxAttempts)", key)
		}
	}
	return nil
}

// JSONSchema declares both accepted forms — the scalar backend-name shorthand
// and the object form — which reflection cannot see past the custom
// marshalers: without this the generated schema said "object" only.
func (SandboxRef) JSONSchema() *jsonschema.Schema {
	policyProperties := jsonschema.NewProperties()
	policyProperties.Set("paths", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"},
		Description: "Path allow/deny list, gitignore syntax with ! negating"})
	policyProperties.Set("maxAttempts", &jsonschema.Schema{Type: "integer", Minimum: json.Number("0"),
		Description: "Bound on submit cycles per task"})

	properties := jsonschema.NewProperties()
	properties.Set("backend", &jsonschema.Schema{Type: "string",
		Description: "Configured sandbox backend from ~/.captain.yaml, or a bare adapter kind"})
	properties.Set("agent", &jsonschema.Schema{Type: "string",
		Description: "Pin one enrolled agent of a git-agent backend"})
	properties.Set("policy", &jsonschema.Schema{Type: "object", Properties: policyProperties, AdditionalProperties: jsonschema.FalseSchema})

	return &jsonschema.Schema{
		Description: "Sandbox the run executes under: a backend name, or an object with overrides",
		OneOf: []*jsonschema.Schema{
			{Type: "string", Description: "Backend name or adapter kind (none, srt, container, git-agent)"},
			{Type: "object", Properties: properties, AdditionalProperties: jsonschema.FalseSchema},
		},
	}
}

// Validate rejects references that resolve to nothing: overrides with no
// backend to apply them to, and a negative attempt bound. Backend-name
// resolution belongs to the config layer, which knows the configured names.
func (r SandboxRef) Validate() error {
	if r.Backend == "" && !r.isScalar() {
		return fmt.Errorf("sandbox overrides (agent/policy) require a backend")
	}
	if r.Policy != nil && r.Policy.MaxAttempts < 0 {
		return fmt.Errorf("sandbox policy maxAttempts must be >= 0, got %d", r.Policy.MaxAttempts)
	}
	return nil
}
