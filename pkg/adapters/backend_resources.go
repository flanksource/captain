package adapters

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/adapters/presentation"
	"github.com/flanksource/captain/pkg/api"
)

func backendResourceSchemas() ([]SchemaDocument, error) {
	raw, err := schemaFiles.ReadFile("resources/captain-backends.json")
	if err != nil {
		return nil, err
	}
	var source map[string]any
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, err
	}
	hooks, err := api.SchemaJSON(struct {
		Sidecar    *api.Workflow `json:"sidecar,omitempty"`
		Supervisor *api.Workflow `json:"supervisor,omitempty"`
	}{})
	if err != nil {
		return nil, err
	}
	var hookSchema map[string]any
	if err := json.Unmarshal(hooks, &hookSchema); err != nil {
		return nil, err
	}
	var documents []SchemaDocument
	for _, name := range []string{"docker", "git-agent"} {
		schema := source["$defs"].(map[string]any)[name].(map[string]any)
		schema["$schema"] = source["$schema"]
		if name == "git-agent" {
			schema["properties"].(map[string]any)["hooks"] = hookSchema
			if definitions, ok := hookSchema["$defs"]; ok {
				schema["$defs"] = definitions
				delete(hookSchema, "$defs")
			}
		}
		presentation.AnnotateSchema(schema, "https://github.com/flanksource/captain/tree/main/pkg/sandbox/adapter")
		document, err := resourceDocument("captain", name+"-backend", schema)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	return documents, nil
}
