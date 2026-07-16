package cli

import (
	"net/http"

	"github.com/flanksource/clicky/rpc"
)

func addCaptainPromptRunPaths(spec *rpc.OpenAPISpec) {
	if spec.Paths == nil {
		spec.Paths = map[string]rpc.OpenAPIPath{}
	}
	pathParameter := rpc.OpenAPIParameter{
		Name: "runId", In: "path", Required: true,
		Schema: &rpc.OpenAPISchema{Type: "string"},
	}
	spec.Paths["/api/captain/prompt/runs/{runId}"] = rpc.OpenAPIPath{
		"get": promptRunOperation("getPromptRun", "Get a prompt run snapshot", pathParameter, nil, jsonResponse(promptRunSnapshotSchema())),
	}
	spec.Paths["/api/captain/prompt/runs/{runId}/stream"] = rpc.OpenAPIPath{
		"get": promptRunOperation("streamPromptRun", "Stream prompt run events", pathParameter, nil, rpc.OpenAPIResponse{
			Description: "SSE events: run, entry, state, done, and error",
			Content: map[string]rpc.OpenAPIMediaType{
				"text/event-stream": {Schema: &rpc.OpenAPISchema{Type: "string"}},
			},
		}),
	}
	messageBody := &rpc.OpenAPIRequestBody{
		Required: true,
		Content: map[string]rpc.OpenAPIMediaType{
			"application/json": {Schema: chatMessageRequestSchema()},
		},
	}
	spec.Paths["/api/captain/prompt/runs/{runId}/message"] = rpc.OpenAPIPath{
		"post": promptRunOperation("messagePromptRun", "Send or queue a prompt run message", pathParameter, messageBody, jsonResponse(chatMessageResponseSchema())),
	}
	spec.Paths["/api/captain/prompt/runs/{runId}/interrupt"] = rpc.OpenAPIPath{
		"post": promptRunOperation("interruptPromptRun", "Pause the active prompt turn", pathParameter, nil, jsonResponse(statusSchema())),
	}
	spec.Paths["/api/captain/prompt/runs/{runId}/stop"] = rpc.OpenAPIPath{
		"post": promptRunOperation("stopPromptRun", "Stop the entire prompt run", pathParameter, nil, jsonResponse(statusSchema())),
	}
	sessionParameter := pathParameter
	sessionParameter.Name = "id"
	spec.Paths["/api/captain/sessions/{id}/message"] = rpc.OpenAPIPath{
		"post": promptRunOperation("messageSession", "Continue a saved or active session", sessionParameter, messageBody, jsonResponse(chatMessageResponseSchema())),
	}
}

func promptRunOperation(
	id, summary string,
	parameter rpc.OpenAPIParameter,
	body *rpc.OpenAPIRequestBody,
	response rpc.OpenAPIResponse,
) rpc.OpenAPIOperation {
	return rpc.OpenAPIOperation{
		Tags: []string{"Prompt runs"}, Summary: summary, OperationID: id,
		Parameters: []rpc.OpenAPIParameter{parameter}, RequestBody: body,
		Responses: map[string]rpc.OpenAPIResponse{
			"202": response,
			"200": response,
			"400": {Description: "Invalid request"},
			"404": {Description: "Run or session not found"},
			"409": {Description: "Run state conflict"},
			"422": {Description: "Session cannot be resumed"},
			"500": {Description: "Provider operation failed"},
		},
	}
}

func jsonResponse(schema *rpc.OpenAPISchema) rpc.OpenAPIResponse {
	return rpc.OpenAPIResponse{
		Description: "Successful response",
		Content: map[string]rpc.OpenAPIMediaType{
			"application/json": {Schema: schema},
		},
	}
}

func chatMessageRequestSchema() *rpc.OpenAPISchema {
	return &rpc.OpenAPISchema{
		Type: "object", Required: []string{"text"},
		Properties: map[string]*rpc.OpenAPISchema{
			"text": {Type: "string"}, "messageId": {Type: "string"},
			"model": {Type: "string"}, "backend": {Type: "string"},
		},
	}
}

func chatMessageResponseSchema() *rpc.OpenAPISchema {
	return &rpc.OpenAPISchema{
		Type: "object", Required: []string{"runId", "messageId", "status", "capabilities"},
		Properties: map[string]*rpc.OpenAPISchema{
			"runId": {Type: "string"}, "messageId": {Type: "string"},
			"status":       {Type: "string", Enum: []any{"steered", "queued", "started"}},
			"capabilities": chatCapabilitiesSchema(),
		},
	}
}

func chatCapabilitiesSchema() *rpc.OpenAPISchema {
	boolean := func() *rpc.OpenAPISchema { return &rpc.OpenAPISchema{Type: "boolean"} }
	return &rpc.OpenAPISchema{
		Type: "object",
		Properties: map[string]*rpc.OpenAPISchema{
			"interrupt": boolean(), "steer": boolean(), "followUp": boolean(), "resume": boolean(),
		},
	}
}

func statusSchema() *rpc.OpenAPISchema {
	return &rpc.OpenAPISchema{
		Type: "object", Required: []string{"status"},
		Properties: map[string]*rpc.OpenAPISchema{"status": {Type: "string"}},
	}
}

func promptRunSnapshotSchema() *rpc.OpenAPISchema {
	return &rpc.OpenAPISchema{
		Type: "object", Required: []string{"run", "entries", "done"},
		Properties: map[string]*rpc.OpenAPISchema{
			"run":     {Type: "object"},
			"entries": {Type: "array", Items: &rpc.OpenAPISchema{Type: "object"}},
			"state":   {Type: "object", Nullable: true},
			"done":    {Type: "boolean"},
			"summary": {Type: "object", Nullable: true},
			"error":   {Type: "string"},
		},
	}
}

func handleCaptainOpenAPI(spec *rpc.OpenAPISpec, yaml bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		var body []byte
		var err error
		if yaml {
			w.Header().Set("Content-Type", "application/yaml")
			body, err = spec.ToYAML()
		} else {
			w.Header().Set("Content-Type", "application/json")
			body, err = spec.ToJSON()
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}
}
