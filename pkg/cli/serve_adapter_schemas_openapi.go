package cli

import "github.com/flanksource/clicky/rpc"

func addAdapterSchemaValidationPath(spec *rpc.OpenAPISpec) {
	issue := &rpc.OpenAPISchema{Type: "object", Required: []string{"instancePath", "schemaPath", "message"},
		Properties: map[string]*rpc.OpenAPISchema{
			"instancePath": {Type: "string"}, "schemaPath": {Type: "string"}, "message": {Type: "string"},
		}}
	spec.Paths["/api/captain/ai/adapter-schemas/validate"] = rpc.OpenAPIPath{"post": {
		Tags: []string{"Adapter schemas"}, Summary: "Validate a local preview value without executing or saving it", OperationID: "validateAdapterSchema",
		RequestBody: &rpc.OpenAPIRequestBody{Required: true, Content: map[string]rpc.OpenAPIMediaType{
			"application/json": {Schema: &rpc.OpenAPISchema{Type: "object", Required: []string{"id", "value"},
				Properties: map[string]*rpc.OpenAPISchema{"id": {Type: "string"}, "value": {}},
			}},
		}},
		Responses: map[string]rpc.OpenAPIResponse{
			"200": jsonResponse(&rpc.OpenAPISchema{Type: "object", Required: []string{"valid", "errors"},
				Properties: map[string]*rpc.OpenAPISchema{
					"valid": {Type: "boolean"}, "errors": {Type: "array", Items: issue},
				}}),
			"400": {Description: "Malformed request, missing value or body larger than 1 MiB"},
			"404": {Description: "Unknown schema id"}, "500": {Description: "Embedded schema is invalid"},
		},
	}}
}
