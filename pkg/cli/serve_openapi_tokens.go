package cli

import (
	"github.com/flanksource/captain/pkg/session/tokens"
	"github.com/flanksource/clicky/rpc"
)

func addCaptainSessionTokenPaths(spec *rpc.OpenAPISpec) {
	if spec.Paths == nil {
		spec.Paths = map[string]rpc.OpenAPIPath{}
	}
	body := rpc.SchemaForStruct(tokens.Options{})
	body.Properties["method"].Enum = []any{"estimate", "provider"}
	body.Required = []string{"method"}
	spec.Paths["/api/captain/sessions/{id}/tokens"] = rpc.OpenAPIPath{
		"post": promptRunOperation("sizeSessionTokens", "Calculate canonical transcript row footprints without changing recorded usage", rpc.OpenAPIParameter{Name: "id", In: "path", Required: true, Schema: &rpc.OpenAPISchema{Type: "string"}}, &rpc.OpenAPIRequestBody{Required: true, Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: body}}}, jsonResponse(rpc.SchemaForStruct(tokens.Result{}))),
	}
}
