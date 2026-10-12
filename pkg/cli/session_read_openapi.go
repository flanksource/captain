package cli

import "github.com/flanksource/clicky/rpc"

func addCaptainSessionReadPaths(spec *rpc.OpenAPISpec) {
	schema := rpc.SchemaForStruct(sessionReadResponse{})
	schema.Required = append(schema.Required, "detailSource")
	schema.Properties["detailAvailable"].Description = "Whether transcript or prompt content is available. False preserves session metadata and other facets."
	schema.Properties["detailSource"].Description = "Source provenance for messages, prompt, turns, usage and approvals."
	spec.Paths["/api/captain/sessions/{id}"] = rpc.OpenAPIPath{"get": {
		Tags: []string{"Sessions"}, Summary: "Get a unified session and its transcript availability", OperationID: "getSession",
		Parameters: []rpc.OpenAPIParameter{
			{Name: "id", In: "path", Required: true, Schema: &rpc.OpenAPISchema{Type: "string"}},
			{Name: "tail", In: "query", Schema: &rpc.OpenAPISchema{Type: "integer"}},
			{Name: "offset", In: "query", Schema: &rpc.OpenAPISchema{Type: "integer"}},
			{Name: "limit", In: "query", Schema: &rpc.OpenAPISchema{Type: "integer"}},
			{Name: "follow", In: "query", Description: "Follow session changes over SSE", Schema: &rpc.OpenAPISchema{Type: "boolean"}},
		},
		Responses: map[string]rpc.OpenAPIResponse{
			"200": {Description: "Unified session, including metadata-only sessions, or an SSE stream", Content: map[string]rpc.OpenAPIMediaType{
				"application/json": {Schema: schema}, "text/event-stream": {Schema: &rpc.OpenAPISchema{Type: "string"}},
			}},
			"400": {Description: "Invalid transcript window"}, "404": {Description: "Session not found"},
			"409": {Description: "Ambiguous session identity"}, "500": {Description: "Session read failed"},
		},
	}}
}
