package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func (s Spec) MarshalJSON() ([]byte, error) {
	value, err := s.wireFields()
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (s Spec) MarshalYAML() (any, error) {
	fields, err := s.wireFields()
	if err != nil {
		return nil, err
	}
	for path := range s.Fields() {
		tokens := strings.Split(strings.TrimPrefix(path, "/"), "/")
		value := serializedField(reflect.ValueOf(s), tokens)
		if value.IsValid() && value.Type() == reflect.TypeOf(json.RawMessage(nil)) {
			putWireField(fields, tokens, []byte(value.Interface().(json.RawMessage)))
		}
	}
	return yamlNumbers(fields)
}

func yamlNumbers(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer, nil
		}
		if integer, err := strconv.ParseUint(typed.String(), 10, 64); err == nil {
			return integer, nil
		}
		return typed.Float64()
	case map[string]any:
		for key, child := range typed {
			converted, err := yamlNumbers(child)
			if err != nil {
				return nil, err
			}
			typed[key] = converted
		}
	case []any:
		for i, child := range typed {
			converted, err := yamlNumbers(child)
			if err != nil {
				return nil, err
			}
			typed[i] = converted
		}
	}
	return value, nil
}

func (s Spec) wireFields() (map[string]any, error) {
	data, err := json.Marshal(s.marshalValue())
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := decodeWireJSON(data, &fields); err != nil {
		return nil, err
	}
	pruneUnownedFields(fields, "", s.Fields())
	paths := make([]string, 0, len(s.explicitFields()))
	for path := range s.explicitFields() {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		tokens := strings.Split(strings.TrimPrefix(path, "/"), "/")
		value := serializedField(reflect.ValueOf(s), tokens)
		if !value.IsValid() {
			return nil, fmt.Errorf("unknown explicit spec field %q", path)
		}
		data, err := json.Marshal(value.Interface())
		if err != nil {
			return nil, err
		}
		var raw any
		if err := decodeWireJSON(data, &raw); err != nil {
			return nil, err
		}
		putWireField(fields, tokens, raw)
	}
	return fields, nil
}

// DecodeFields lets the enclosing decoder choose its unknown-field policy.
func (Spec) DecodeFields() any { return specMarshal{} }

func (s *Spec) UnmarshalJSON(data []byte) error {
	var wire specMarshal
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*s = wire.toSpec()
	return s.capturePresence(data)
}

func (s *Spec) UnmarshalYAML(node *yaml.Node) error {
	data, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	var wire specMarshal
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*s = wire.toSpec()
	var fields any
	if err := node.Decode(&fields); err != nil {
		return err
	}
	s.Explicit = FieldPresence{}
	capturePresence(fields, "", s.Explicit)
	s.pruneOpaquePresence()
	if len(s.Explicit) == 0 {
		s.Explicit = nil
	}
	return nil
}

func (wire specMarshal) toSpec() Spec {
	s := Spec{Model: Model(wire.ModelFields), Messages: wire.Messages,
		ToolPolicy: wire.ToolPolicy, ToolApproval: wire.Approval, Setup: wire.Setup,
		Sandbox: wire.Sandbox, Workflow: wire.Workflow, SessionID: wire.SessionID, CLIArgs: wire.CLIArgs}
	if wire.Prompt != nil {
		s.Prompt = *wire.Prompt
	}
	if wire.Budget != nil {
		s.Budget = *wire.Budget
	}
	if wire.Memory != nil {
		s.Memory = *wire.Memory
	}
	if wire.Permissions != nil {
		s.Permissions = *wire.Permissions
	}
	if wire.Preferences != nil {
		s.ToolPreferences = *wire.Preferences
	}
	return s
}

func serializedField(value reflect.Value, tokens []string) reflect.Value {
	if len(tokens) == 0 {
		return value
	}
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return value
	}
	token := unescapeField(tokens[0])
	if value.CanInterface() {
		if _, ok := value.Interface().(MCP); ok {
			switch token {
			case "disabled":
				return serializedField(value.FieldByName("Disabled"), tokens[1:])
			case "servers":
				return serializedField(value.FieldByName("Servers"), tokens[1:])
			default:
				return serializedField(value.FieldByName("Modes"), tokens)
			}
		}
	}
	switch value.Kind() {
	case reflect.Struct:
		for i := range value.NumField() {
			field := value.Type().Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" || !field.IsExported() {
				continue
			}
			if field.Anonymous && name == "" {
				if found := serializedField(value.Field(i), tokens); found.IsValid() {
					return found
				}
			} else if name == token {
				return serializedField(value.Field(i), tokens[1:])
			}
		}
	case reflect.Map:
		if value.Type().Key().Kind() == reflect.String {
			return serializedField(value.MapIndex(reflect.ValueOf(token).Convert(value.Type().Key())), tokens[1:])
		}
	case reflect.Slice, reflect.Array:
		if index, err := strconv.Atoi(token); err == nil && index >= 0 && index < value.Len() {
			return serializedField(value.Index(index), tokens[1:])
		}
	}
	return reflect.Value{}
}

func putWireField(value any, tokens []string, field any) {
	key := unescapeField(tokens[0])
	switch typed := value.(type) {
	case map[string]any:
		if len(tokens) == 1 {
			typed[key] = field
			return
		}
		if typed[key] == nil {
			typed[key] = map[string]any{}
		}
		putWireField(typed[key], tokens[1:], field)
	case []any:
		index, err := strconv.Atoi(key)
		if err != nil || index < 0 || index >= len(typed) {
			return
		}
		if len(tokens) == 1 {
			typed[index] = field
		} else {
			putWireField(typed[index], tokens[1:], field)
		}
	}
}

func fmtFieldIndex(path string, index int) string { return path + "/" + strconv.Itoa(index) }

func pruneUnownedFields(value any, path string, present FieldPresence) {
	switch fields := value.(type) {
	case map[string]any:
		for key, child := range fields {
			childPath := path + "/" + escapeField(key)
			pruneUnownedFields(child, childPath, present)
			empty := child == nil
			if child != nil {
				v := reflect.ValueOf(child)
				empty = v.IsZero() || (v.Kind() == reflect.Map || v.Kind() == reflect.Slice) && v.Len() == 0
				if number, ok := child.(json.Number); ok {
					parsed, err := number.Float64()
					empty = err == nil && parsed == 0
				}
			}
			if empty && !fieldCovered(present, childPath) {
				delete(fields, key)
			}
		}
	case []any:
		for i, child := range fields {
			pruneUnownedFields(child, fmtFieldIndex(path, i), present)
		}
	}
}

func decodeWireJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(value)
}

func validateFallbackJSON(data []byte) error {
	var fields any
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	return validateFallbackValues(fields)
}

func validateFallbackValues(value any) error {
	fields, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	fallbacks, _ := fields["fallbacks"].([]any)
	for i, fallback := range fallbacks {
		object, ok := fallback.(map[string]any)
		if !ok {
			continue
		}
		data, err := json.Marshal(object)
		if err != nil {
			return err
		}
		var wire specModelWire
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&wire); err != nil {
			return fmt.Errorf("fallback[%d]: %w", i, err)
		}
		if err := validateFallbackValues(object); err != nil {
			return fmt.Errorf("fallback[%d]: %w", i, err)
		}
	}
	return nil
}
