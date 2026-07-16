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

func addCaptainProviderTokenPaths(spec *rpc.OpenAPISpec) {
	if spec.Paths == nil {
		spec.Paths = map[string]rpc.OpenAPIPath{}
	}
	provider := rpc.OpenAPIParameter{
		Name: "provider", In: "path", Required: true,
		Schema: &rpc.OpenAPISchema{Type: "string", Enum: []any{"anthropic", "openai", "gemini", "deepseek"}},
	}
	tokenSchema := &rpc.OpenAPISchema{Type: "string", Format: "password", Extensions: map[string]any{"writeOnly": true}}
	request := func(required bool) *rpc.OpenAPIRequestBody {
		schema := &rpc.OpenAPISchema{Type: "object", Properties: map[string]*rpc.OpenAPISchema{"token": tokenSchema}}
		if required {
			schema.Required = []string{"token"}
		}
		return &rpc.OpenAPIRequestBody{Required: true, Content: map[string]rpc.OpenAPIMediaType{
			"application/json": {Schema: schema},
		}}
	}
	operation := func(id, summary string, body *rpc.OpenAPIRequestBody) rpc.OpenAPIOperation {
		return rpc.OpenAPIOperation{
			Tags: []string{"Provider credentials"}, Summary: summary, OperationID: id,
			Parameters: []rpc.OpenAPIParameter{provider}, RequestBody: body,
			Responses: map[string]rpc.OpenAPIResponse{
				"200": jsonResponse(providerTokenResponseSchema()),
				"400": {Description: "Invalid request"}, "403": {Description: "Local same-origin access required"},
				"422": {Description: "Credential rejected"}, "500": {Description: "Vault persistence failed"},
				"502": {Description: "Provider request failed"},
			},
		}
	}
	spec.Paths["/api/captain/ai/providers/{provider}/token"] = rpc.OpenAPIPath{
		"put": operation("saveProviderToken", "Validate and save a provider token", request(true)),
	}
	spec.Paths["/api/captain/ai/providers/{provider}/token/test"] = rpc.OpenAPIPath{
		"post": operation("testProviderToken", "Test a candidate or configured provider token", request(false)),
	}
}

func providerTokenResponseSchema() *rpc.OpenAPISchema {
	return &rpc.OpenAPISchema{Type: "object", Required: []string{"provider", "valid", "saved", "source", "maskedToken", "modelCount"}, Properties: map[string]*rpc.OpenAPISchema{
		"provider": {Type: "string"}, "valid": {Type: "boolean"}, "saved": {Type: "boolean"},
		"source": {Type: "string"}, "maskedToken": {Type: "string"}, "modelCount": {Type: "integer"},
	}}
}

func addCaptainProviderDefaultsPaths(spec *rpc.OpenAPISpec) {
	if spec.Paths == nil {
		spec.Paths = map[string]rpc.OpenAPIPath{}
	}
	provider := rpc.OpenAPIParameter{
		Name: "provider", In: "path", Required: true,
		Schema: &rpc.OpenAPISchema{Type: "string", Enum: []any{"anthropic", "openai", "gemini", "deepseek"}},
	}
	stringField := func() *rpc.OpenAPISchema { return &rpc.OpenAPISchema{Type: "string"} }
	defaultsRequest := &rpc.OpenAPIRequestBody{Required: true, Content: map[string]rpc.OpenAPIMediaType{
		"application/json": {Schema: &rpc.OpenAPISchema{
			Type: "object", Required: []string{"agent", "model", "effort"},
			Properties: map[string]*rpc.OpenAPISchema{"agent": stringField(), "model": stringField(), "effort": stringField()},
		}},
	}}
	defaultsResponse := &rpc.OpenAPISchema{
		Type: "object", Required: []string{"provider", "agent", "model", "effort", "active"},
		Properties: map[string]*rpc.OpenAPISchema{
			"provider": stringField(), "agent": stringField(), "model": stringField(),
			"effort": stringField(), "active": {Type: "boolean"},
		},
	}
	spec.Paths["/api/captain/ai/providers/{provider}/defaults"] = rpc.OpenAPIPath{"put": {
		Tags: []string{"Provider configuration"}, Summary: "Save provider runtime defaults", OperationID: "saveProviderDefaults",
		Parameters: []rpc.OpenAPIParameter{provider}, RequestBody: defaultsRequest,
		Responses: map[string]rpc.OpenAPIResponse{
			"200": jsonResponse(defaultsResponse), "400": {Description: "Invalid request"},
			"403": {Description: "Local same-origin access required"}, "422": {Description: "Defaults rejected"},
			"500": {Description: "Configuration persistence failed"},
		},
	}}
	activeRequest := &rpc.OpenAPIRequestBody{Required: true, Content: map[string]rpc.OpenAPIMediaType{
		"application/json": {Schema: &rpc.OpenAPISchema{
			Type: "object", Required: []string{"provider"}, Properties: map[string]*rpc.OpenAPISchema{"provider": stringField()},
		}},
	}}
	spec.Paths["/api/captain/ai/default-provider"] = rpc.OpenAPIPath{"put": {
		Tags: []string{"Provider configuration"}, Summary: "Select the provider for flagless runs", OperationID: "saveDefaultProvider",
		RequestBody: activeRequest, Responses: map[string]rpc.OpenAPIResponse{
			"200": jsonResponse(&rpc.OpenAPISchema{Type: "object", Required: []string{"provider"}, Properties: map[string]*rpc.OpenAPISchema{"provider": stringField()}}),
			"400": {Description: "Invalid request"}, "403": {Description: "Local same-origin access required"},
			"500": {Description: "Configuration persistence failed"},
		},
	}}
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
