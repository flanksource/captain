package api

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/flanksource/commons-db/workspace"
	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// WORKAROUND(gavel-commit-compat): gavel still drives commits through Hook.Do and gates: full/cheap
// Correct fix: gavel supplies HostMessage/HostGates/HostHooks + a push wrapper; then delete this layer
// Ref: discussed with user 2026-10-28
//
// CommitGateSpecs is the gate list of a commit policy. Besides the list form it
// accepts the retired level strings: none is no gates, cheap the two gates the
// old cheap level ran, and full the FullPipelineGate directive that hands the
// commit to a host's Hook.Do.
type CommitGateSpecs []workspace.GateSpec

// FullPipelineGate is a directive, not a registered gate: the host's Hook.Do
// runs its whole pre-commit pipeline. It is stripped before gates resolve.
const FullPipelineGate = "full"

// legacyMaxFileSize is the per-file ceiling the retired cheap level enforced. A
// float64, as JSON decodes numbers: specs round-trip through JSON when merged,
// and an int here would compare unequal to its own round-trip.
const legacyMaxFileSize float64 = 1 << 20

var (
	CommitGatesNone  = CommitGateSpecs{}
	CommitGatesCheap = CommitGateSpecs{
		{Name: workspace.SecretNamesGateName},
		{Name: workspace.MaxSizeGateName, Config: map[string]any{"bytes": legacyMaxFileSize}},
	}
	CommitGatesFull = CommitGateSpecs{{Name: FullPipelineGate}}
)

func legacyCommitGates(level string) (CommitGateSpecs, error) {
	switch level {
	case "none":
		return slices.Clone(CommitGatesNone), nil
	case "cheap":
		return slices.Clone(CommitGatesCheap), nil
	case "full":
		return slices.Clone(CommitGatesFull), nil
	}
	return nil, fmt.Errorf("invalid commit gates %q; want a list of gates, or one of: none, cheap, full", level)
}

// HasGate reports whether specs names the gate.
func HasGate(specs []workspace.GateSpec, name string) bool {
	return slices.ContainsFunc(specs, func(spec workspace.GateSpec) bool { return spec.Name == name })
}

// Resolvable is the list without the FullPipelineGate directive: what
// workspace.ResolveGates can build.
func (g CommitGateSpecs) Resolvable() []workspace.GateSpec {
	return slices.DeleteFunc(slices.Clone([]workspace.GateSpec(g)), func(spec workspace.GateSpec) bool {
		return spec.Name == FullPipelineGate
	})
}

func (g *CommitGateSpecs) UnmarshalJSON(data []byte) error {
	var level string
	if err := json.Unmarshal(data, &level); err == nil {
		specs, err := legacyCommitGates(level)
		*g = specs
		return err
	}
	var specs []workspace.GateSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return fmt.Errorf("commit gates: %w", err)
	}
	*g = specs
	return nil
}

func (g *CommitGateSpecs) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		specs, err := legacyCommitGates(value.Value)
		*g = specs
		return err
	}
	var specs []workspace.GateSpec
	if err := value.Decode(&specs); err != nil {
		return fmt.Errorf("commit gates: %w", err)
	}
	*g = specs
	return nil
}

// JSONSchema accepts the list form and the retired level strings.
func (CommitGateSpecs) JSONSchema() *jsonschema.Schema {
	gate := &jsonschema.Schema{
		Type:       "object",
		Required:   []string{"name"},
		Properties: jsonschema.NewProperties(),
	}
	gate.Properties.Set("name", &jsonschema.Schema{Type: "string", Description: "Registered gate name: secret-names, max-size, or a host's own."})
	gate.Properties.Set("config", &jsonschema.Schema{Type: "object", Description: "Gate configuration, e.g. max-size's bytes."})
	return &jsonschema.Schema{
		Description: "Gates each commit must pass. The excludes gate always runs. Retired level strings none, cheap and full are still accepted.",
		OneOf: []*jsonschema.Schema{
			{Type: "array", Items: gate},
			{Type: "string", Enum: []any{"none", "cheap", "full"}},
		},
	}
}
