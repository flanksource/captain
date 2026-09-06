package captainconfig

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
	"gopkg.in/yaml.v3"
)

func (a AIDefaults) WithExplicit(paths ...string) AIDefaults {
	a.Explicit = a.Explicit.Clone()
	if a.Explicit == nil {
		a.Explicit = registry.FieldPresence{}
	}
	for _, path := range paths {
		a.Explicit[path] = true
	}
	return a
}

// Fields distinguishes an omitted global setting from an authored zero value.
func (a AIDefaults) Fields() registry.FieldPresence {
	fields := registry.FieldPresence{}
	for key := range a.fieldValues() {
		fields["/"+key] = true
	}
	return fields
}

func (a AIDefaults) fieldValues() map[string]any {
	out := map[string]any{}
	value := reflect.ValueOf(a)
	for i := range value.NumField() {
		name := strings.Split(value.Type().Field(i).Tag.Get("yaml"), ",")[0]
		if name == "-" || name == "" {
			continue
		}
		field := value.Field(i)
		if !field.IsZero() || a.Explicit["/"+name] {
			out[name] = field.Interface()
		}
	}
	return out
}

func (a AIDefaults) MarshalJSON() ([]byte, error) { return json.Marshal(a.fieldValues()) }

func (a AIDefaults) MarshalYAML() (any, error) { return a.fieldValues(), nil }

type defaultsWire AIDefaults

func (a *AIDefaults) UnmarshalJSON(data []byte) error {
	var value defaultsWire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*a = AIDefaults(value)
	a.captureFields(fields)
	return nil
}

func (a *AIDefaults) UnmarshalYAML(node *yaml.Node) error {
	var value defaultsWire
	if err := node.Decode(&value); err != nil {
		return err
	}
	var fields map[string]any
	if err := node.Decode(&fields); err != nil {
		return err
	}
	*a = AIDefaults(value)
	a.captureFields(fields)
	return nil
}

func (a *AIDefaults) captureFields(fields map[string]any) {
	a.Explicit = registry.FieldPresence{}
	typeOf := reflect.TypeOf(*a)
	for i := range typeOf.NumField() {
		name := strings.Split(typeOf.Field(i).Tag.Get("yaml"), ",")[0]
		if _, present := fields[name]; present && name != "-" {
			a.Explicit["/"+name] = true
		}
	}
}
