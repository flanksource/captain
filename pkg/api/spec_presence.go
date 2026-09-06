package api

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
)

type FieldPresence = registry.FieldPresence

type specModelWire Model

// ModelFields exposes the plain model shape to enclosing field inspectors.
type ModelFields = specModelWire

// WithExplicit marks zero values authored by a programmatic caller.
func (s Spec) WithExplicit(paths ...string) Spec {
	s.Explicit = s.Explicit.Clone()
	if s.Explicit == nil {
		s.Explicit = FieldPresence{}
	}
	for _, path := range paths {
		s.Explicit[path] = true
	}
	return s
}

// Fields reports authored values, including explicitly present zero values.
func (s Spec) Fields() FieldPresence {
	fields := s.explicitFields()
	collectPresentFields(reflect.ValueOf(s), "", fields)
	return fields
}

func (s Spec) explicitFields() FieldPresence {
	fields := s.Explicit.Clone()
	if fields == nil {
		fields = FieldPresence{}
	}
	for path, present := range s.Model.Explicit {
		fields[path] = present
	}
	return fields
}

func collectPresentFields(value reflect.Value, path string, fields FieldPresence) {
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return
		}
		value = value.Elem()
		if value.IsZero() && path != "" {
			fields[path] = true
		}
	}
	if !value.IsValid() {
		return
	}
	if value.CanInterface() {
		if raw, ok := value.Interface().(json.RawMessage); ok {
			if len(raw) > 0 {
				fields[path] = true
			}
			return
		}
		if mcp, ok := value.Interface().(MCP); ok {
			if mcp.Disabled {
				fields[path+"/disabled"] = true
			}
			if len(mcp.Servers) > 0 {
				fields[path+"/servers"] = true
			}
			for name := range mcp.Modes {
				fields[path+"/"+escapeField(name)] = true
			}
			return
		}
		if model, ok := value.Interface().(Model); ok {
			for field := range model.Fields() {
				fields[path+field] = true
			}
			for i, fallback := range model.Fallbacks {
				collectPresentFields(reflect.ValueOf(fallback), fmtFieldIndex(path+"/fallbacks", i), fields)
			}
			return
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
				collectPresentFields(value.Field(i), path, fields)
			} else if name != "" {
				collectPresentFields(value.Field(i), path+"/"+escapeField(name), fields)
			}
		}
	case reflect.Map:
		for _, key := range value.MapKeys() {
			fieldPath := path + "/" + escapeField(key.String())
			fields[fieldPath] = true
			collectPresentFields(value.MapIndex(key), fieldPath, fields)
		}
	case reflect.Slice, reflect.Array:
		if value.Len() > 0 || value.Kind() == reflect.Slice && !value.IsNil() {
			fields[path] = true
		}
		for i := range value.Len() {
			collectPresentFields(value.Index(i), fmtFieldIndex(path, i), fields)
		}
	default:
		if !value.IsZero() {
			fields[path] = true
		}
	}
}

func capturePresence(value any, path string, fields FieldPresence) {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 && path != "" {
			fields[path] = true
		}
		for key, child := range typed {
			capturePresence(child, path+"/"+escapeField(key), fields)
		}
	case []any:
		if len(typed) == 0 {
			fields[path] = true
		}
		for i, child := range typed {
			capturePresence(child, fmtFieldIndex(path, i), fields)
		}
	default:
		if value == nil || reflect.ValueOf(value).IsZero() {
			fields[path] = true
		}
	}
}

func (s *Spec) capturePresence(data []byte) error {
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
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

func (s *Spec) pruneOpaquePresence() {
	for path := range s.Explicit {
		tokens := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if !serializedField(reflect.ValueOf(*s), tokens).IsValid() {
			delete(s.Explicit, path)
			continue
		}
		for i := 1; i < len(tokens); i++ {
			parent := serializedField(reflect.ValueOf(*s), tokens[:i])
			if parent.IsValid() && parent.Type() == reflect.TypeOf(json.RawMessage(nil)) {
				delete(s.Explicit, path)
				break
			}
		}
	}
}

func escapeField(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func unescapeField(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~1", "/"), "~0", "~")
}
