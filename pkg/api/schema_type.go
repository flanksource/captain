package api

import "fmt"

type SchemaType string

const (
	SchemaTypeJSONSchema SchemaType = "json-schema"
	SchemaTypeTypeSafe   SchemaType = "typesafe-schema"
)

func (t SchemaType) ValidateInput() error {
	if t == "" || t == SchemaTypeJSONSchema {
		return nil
	}
	return fmt.Errorf("input schema type %q is unsupported; want json-schema", t)
}

func (t SchemaType) ValidateOutput() error {
	if t == "" || t == SchemaTypeJSONSchema || t == SchemaTypeTypeSafe {
		return nil
	}
	return fmt.Errorf("output schema type %q is unsupported", t)
}
