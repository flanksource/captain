package registry

import (
	"encoding/json"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// FieldPresence records explicitly authored JSON-pointer paths. It is runtime
// metadata; serializers preserve the corresponding values, not this map.
type FieldPresence map[string]bool

func (f FieldPresence) Has(path string) bool { return f[path] }

func (f FieldPresence) Clone() FieldPresence {
	if f == nil {
		return nil
	}
	out := make(FieldPresence, len(f))
	for path, present := range f {
		out[path] = present
	}
	return out
}

// WithExplicit marks authored zero values in programmatically constructed models.
func (m Model) WithExplicit(paths ...string) Model {
	m.Explicit = m.Explicit.Clone()
	if m.Explicit == nil {
		m.Explicit = FieldPresence{}
	}
	for _, path := range paths {
		m.Explicit[path] = true
	}
	return m
}

// Fields includes both explicit zero values and nonzero Go-authored fields.
func (m Model) Fields() FieldPresence {
	fields := FieldPresence{}
	for key := range m.fieldValues() {
		fields["/"+key] = true
	}
	return fields
}

func (m Model) fieldValues() map[string]any {
	out := map[string]any{}
	value := reflect.ValueOf(m)
	for i := range value.NumField() {
		name := strings.Split(value.Type().Field(i).Tag.Get("json"), ",")[0]
		if name == "-" || name == "" {
			continue
		}
		field := value.Field(i)
		if !field.IsZero() || m.Explicit["/"+name] {
			out[name] = field.Interface()
		}
	}
	return out
}

func (m Model) MarshalJSON() ([]byte, error) { return json.Marshal(m.fieldValues()) }

func (m Model) MarshalYAML() (any, error) { return m.fieldValues(), nil }

type modelWire Model

func (Model) DecodeFields() any { return modelWire{} }

func (ModelList) DecodeFields() any { return []modelWire{} }

func (m *Model) UnmarshalJSON(data []byte) error {
	var value modelWire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = Model(value)
	m.captureFields(fields)
	return nil
}

func (m *Model) UnmarshalYAML(node *yaml.Node) error {
	var value modelWire
	if err := node.Decode(&value); err != nil {
		return err
	}
	var fields map[string]any
	if err := node.Decode(&fields); err != nil {
		return err
	}
	*m = Model(value)
	m.captureFields(fields)
	return nil
}

func (m *Model) captureFields(fields map[string]any) {
	m.Explicit = FieldPresence{}
	typeOf := reflect.TypeOf(*m)
	for i := range typeOf.NumField() {
		name := strings.Split(typeOf.Field(i).Tag.Get("json"), ",")[0]
		if _, present := fields[name]; present && name != "-" {
			m.Explicit["/"+name] = true
		}
	}
}

func mergeExplicitModel(merged, override Model) Model {
	out := reflect.ValueOf(&merged).Elem()
	value := reflect.ValueOf(override)
	for i := range value.NumField() {
		name := strings.Split(value.Type().Field(i).Tag.Get("json"), ",")[0]
		if override.Explicit["/"+name] && value.Field(i).IsZero() {
			out.Field(i).Set(value.Field(i))
		}
	}
	if override.Fallbacks != nil && len(override.Fallbacks) == 0 {
		merged.Fallbacks = ModelList{}
	}
	return merged
}
