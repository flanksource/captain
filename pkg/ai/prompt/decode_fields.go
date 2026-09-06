package prompt

import (
	"bytes"
	"fmt"
	"reflect"

	"github.com/flanksource/captain/pkg/api"
	"gopkg.in/yaml.v3"
)

func validateDeclarationFields(data []byte, fields any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(reflect.New(reflect.TypeOf(fields)).Interface()); err != nil {
		return err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	fallbacks, _ := raw["fallbacks"].([]any)
	for i, fallback := range fallbacks {
		if _, object := fallback.(map[string]any); !object {
			continue
		}
		encoded, err := yaml.Marshal(fallback)
		if err != nil {
			return err
		}
		if err := validateDeclarationFields(encoded, (api.Model{}).DecodeFields()); err != nil {
			return fmt.Errorf("fallback %d: %w", i+1, err)
		}
	}
	return nil
}
