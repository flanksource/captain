package api

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
)

func (s SchemaStrictness) Canonical() SchemaStrictness {
	if s == "retry" {
		return SchemaStrictnessRepair
	}
	return s
}

func (s SchemaStrictness) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(s.Canonical()))
}

func (s *SchemaStrictness) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	policy := SchemaStrictness(value).Canonical()
	if err := policy.Validate(); err != nil {
		return err
	}
	*s = policy
	return nil
}

func (s SchemaStrictness) MarshalYAML() (any, error) {
	return string(s.Canonical()), s.Validate()
}

func (s *SchemaStrictness) UnmarshalYAML(node *yaml.Node) error {
	var value string
	if err := node.Decode(&value); err != nil {
		return err
	}
	policy := SchemaStrictness(value).Canonical()
	if err := policy.Validate(); err != nil {
		return err
	}
	*s = policy
	return nil
}
