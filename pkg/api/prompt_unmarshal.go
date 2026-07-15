package api

import (
	"bytes"
	"encoding/json"

	"gopkg.in/yaml.v3"
)

// UnmarshalJSON accepts either a bare string — shorthand for {user: <string>} —
// or the full object form. This lets a config scalar carry just the user prompt
// (`prompt: "review strictly"`) while still supporting the structured form
// (`prompt: {user: ..., system: ...}`).
func (p *Prompt) UnmarshalJSON(data []byte) error {
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '"' {
		var user string
		if err := json.Unmarshal(trimmed, &user); err != nil {
			return err
		}
		*p = Prompt{User: user}
		return nil
	}
	type promptAlias Prompt
	var a promptAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*p = Prompt(a)
	return nil
}

// UnmarshalYAML mirrors UnmarshalJSON: a scalar node is the user prompt, a
// mapping node is the full object.
func (p *Prompt) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode && value.Tag != "!!null" {
		var user string
		if err := value.Decode(&user); err != nil {
			return err
		}
		*p = Prompt{User: user}
		return nil
	}
	type promptAlias Prompt
	var a promptAlias
	if err := value.Decode(&a); err != nil {
		return err
	}
	*p = Prompt(a)
	return nil
}
