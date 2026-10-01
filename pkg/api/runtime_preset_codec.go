package api

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
)

func (RuntimePresetSpec) DecodeFields() any { return Spec{}.DecodeFields() }

func (s RuntimePresetSpec) MarshalJSON() ([]byte, error) { return json.Marshal(s.ToSpec()) }

func (s RuntimePresetSpec) MarshalYAML() (any, error) { return s.ToSpec().MarshalYAML() }

func (s *RuntimePresetSpec) UnmarshalJSON(data []byte) error {
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return err
	}
	*s = RuntimePresetSpec(spec)
	return nil
}

func (s *RuntimePresetSpec) UnmarshalYAML(node *yaml.Node) error {
	var spec Spec
	if err := node.Decode(&spec); err != nil {
		return err
	}
	*s = RuntimePresetSpec(spec)
	return nil
}
