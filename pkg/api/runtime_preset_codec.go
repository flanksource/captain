package api

import (
	"bytes"
	"encoding/json"

	"gopkg.in/yaml.v3"
)

type runtimePresetWire struct {
	ModelFields     `json:",inline" yaml:",inline"`
	Budget          Budget              `json:"budget,omitempty" yaml:"budget,omitempty"`
	Memory          Memory              `json:"memory,omitempty" yaml:"memory,omitempty"`
	Permissions     Permissions         `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	ToolPreferences ToolPreferences     `json:"toolPreferences,omitempty" yaml:"toolPreferences,omitempty"`
	ToolPolicy      PermissionPolicy    `json:"toolPolicy,omitempty" yaml:"toolPolicy,omitempty"`
	Setup           *RuntimePresetSetup `json:"setup,omitempty" yaml:"setup,omitempty"`
	Sandbox         *SandboxRef         `json:"sandbox,omitempty" yaml:"sandbox,omitempty"`
}

func (RuntimePresetSpec) DecodeFields() any { return runtimePresetWire{} }

func (s RuntimePresetSpec) MarshalJSON() ([]byte, error) { return json.Marshal(s.ToSpec()) }

func (s RuntimePresetSpec) MarshalYAML() (any, error) { return s.ToSpec().MarshalYAML() }

func (s *RuntimePresetSpec) UnmarshalJSON(data []byte) error {
	if err := validateFallbackJSON(data); err != nil {
		return err
	}
	var wire runtimePresetWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*s = wire.toPreset()
	spec := s.ToSpec()
	if err := spec.capturePresence(data); err != nil {
		return err
	}
	s.Explicit = spec.Explicit
	return nil
}

func (s *RuntimePresetSpec) UnmarshalYAML(node *yaml.Node) error {
	data, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	var wire runtimePresetWire
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*s = wire.toPreset()
	var fields any
	if err := node.Decode(&fields); err != nil {
		return err
	}
	if err := validateFallbackValues(fields); err != nil {
		return err
	}
	explicit := FieldPresence{}
	capturePresence(fields, "", explicit)
	if len(explicit) > 0 {
		spec := s.ToSpec()
		spec.Explicit = explicit
		spec.pruneOpaquePresence()
		s.Explicit = spec.Explicit
	}
	return nil
}

func (wire runtimePresetWire) toPreset() RuntimePresetSpec {
	return RuntimePresetSpec{Model: Model(wire.ModelFields), Budget: wire.Budget, Memory: wire.Memory,
		Permissions: wire.Permissions, ToolPreferences: wire.ToolPreferences, ToolPolicy: wire.ToolPolicy,
		Setup: wire.Setup, Sandbox: wire.Sandbox}
}
