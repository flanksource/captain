package api

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
)

// Schema reflects v into a JSON Schema, referencing nested types under $defs,
// honouring `jsonschema:"required,enum=,minimum="` constraints, and using the
// json field names as property names (which match the yaml names byte-for-byte).
func Schema(v any) *jsonschema.Schema {
	r := &jsonschema.Reflector{
		FieldNameTag:               "json",
		RequiredFromJSONSchemaTags: true,
	}
	return r.Reflect(v)
}

// SchemaJSON marshals Schema(v) to indented JSON.
func SchemaJSON(v any) ([]byte, error) {
	return json.MarshalIndent(Schema(v), "", "  ")
}
