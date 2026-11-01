package adapters

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/adapters/presentation"
	"github.com/flanksource/captain/pkg/api"
)

type resourceProjection struct {
	provider string
	name     string
	pointer  string
}

var resourceProjections = []resourceProjection{
	{"openai", "config", ""},
	{"openai", "permission-profile", "/definitions/PermissionProfileToml"},
	{"openai", "filesystem", "/definitions/FilesystemPermissionsToml"},
	{"openai", "network", "/definitions/NetworkToml"},
	{"openai", "sandbox", "/definitions/SandboxWorkspaceWrite"},
	{"openai", "mcp-server", "/definitions/RawMcpServerConfig"},
	{"openai", "hooks", "/definitions/HooksToml"},
	{"openai", "agent-definition", "/definitions/AgentRoleToml"},
	{"openai", "profile", "/definitions/ConfigProfile"},
	{"anthropic", "settings", ""},
	{"anthropic", "sandbox", "/definitions/Settings/properties/sandbox"},
	{"anthropic", "permissions", "/definitions/Settings/properties/permissions"},
	{"anthropic", "hooks", "/definitions/Settings/properties/hooks"},
	{"anthropic", "skills", "/definitions/Settings/properties/skillOverrides"},
	{"anthropic", "plugins", "/definitions/Settings/properties/enabledPlugins"},
	{"anthropic", "mcp-server", "/definitions/McpServerConfigForProcessTransport"},
	{"anthropic", "agent-definition", "/definitions/AgentDefinition"},
	{"anthropic", "plugin", "/definitions/SdkPluginConfig"},
	{"google", "settings", ""},
	{"google", "sandbox", "/properties/tools/properties/sandbox"},
	{"google", "permissions", "/properties/security"},
	{"google", "mcp-servers", "/properties/mcpServers"},
	{"google", "hooks", "/properties/hooks"},
	{"google", "agents", "/properties/agents"},
	{"google", "skills", "/properties/skills"},
	{"google", "extensions", "/properties/extensions"},
	{"google", "model", "/properties/model"},
	{"google", "context", "/properties/context"},
	{"google", "environment", "/properties/advanced"},
}

func resourceSchemas() ([]SchemaDocument, error) {
	sources := map[string]map[string]any{}
	for _, provider := range []string{"openai", "anthropic", "google"} {
		raw, err := schemaFiles.ReadFile("resources/" + provider + ".json")
		if err != nil {
			return nil, fmt.Errorf("read %s resource schema: %w", provider, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, fmt.Errorf("decode %s resources: %w", provider, err)
		}
		sources[provider] = schema
	}
	var documents []SchemaDocument
	for _, projection := range resourceProjections {
		document, err := projectResource(sources[projection.provider], projection)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	portable, err := captainResourceSchemas()
	if err != nil {
		return nil, err
	}
	documents = append(documents, portable...)
	backends, err := backendResourceSchemas()
	return append(documents, backends...), err
}

func projectResource(source map[string]any, projection resourceProjection) (SchemaDocument, error) {
	selected, err := schemaPointer(source, projection.pointer)
	if err != nil {
		return SchemaDocument{}, fmt.Errorf("resource %s/%s: %w", projection.provider, projection.name, err)
	}
	schema := map[string]any{}
	for key, value := range selected {
		schema[key] = value
	}
	for _, key := range []string{"$schema", "definitions", "$defs", "x-captain-resource"} {
		if value, ok := source[key]; ok {
			schema[key] = value
		}
	}
	schema["title"] = presentation.Title(projection.provider) + " " + presentation.Title(projection.name)
	if schema["description"] == nil {
		schema["description"] = fmt.Sprintf("Native %s %s configuration.", projection.provider, projection.name)
	}
	return resourceDocument(projection.provider, projection.name, schema)
}

func schemaPointer(root map[string]any, pointer string) (map[string]any, error) {
	node := root
	for _, key := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if pointer == "" {
			break
		}
		key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
		child, ok := node[key].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema pointer %q has no object at %q", pointer, key)
		}
		node = child
	}
	return node, nil
}

func resourceDocument(provider, name string, schema map[string]any) (SchemaDocument, error) {
	pruneDefinitions(schema)
	raw, err := json.Marshal(schema)
	if err != nil {
		return SchemaDocument{}, fmt.Errorf("encode resource %s/%s: %w", provider, name, err)
	}
	kind := "resource"
	if name == "config" || name == "settings" {
		kind = "configuration"
	}
	title, _ := schema["title"].(string)
	description, _ := schema["description"].(string)
	return SchemaDocument{ID: provider + "/" + name, Kind: kind, Provider: provider,
		Title: title, Description: description, Schema: raw}, nil
}

func captainResourceSchemas() ([]SchemaDocument, error) {
	resources := []struct {
		name  string
		value any
	}{
		{"sandbox", api.SandboxRef{}}, {"sandbox-policy", api.NativeSandboxPolicy{}},
		{"permissions", api.Permissions{}}, {"preset", api.RuntimePreset{}},
		{"spec", api.Spec{}}, {"tools", api.Tools{}}, {"mcp", api.MCP{}},
		{"resource-policy", api.ResourcePolicies{}}, {"dispatch", api.SandboxDispatchPolicy{}},
	}
	var documents []SchemaDocument
	for _, resource := range resources {
		raw, err := api.SchemaJSON(resource.value)
		if err != nil {
			return nil, err
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, err
		}
		schema["title"] = "Captain " + presentation.Title(resource.name)
		schema["description"] = "Portable Captain " + presentation.Title(resource.name) + " configuration. Runtime capabilities determine which settings can be enforced."
		presentation.AnnotateSchema(schema, "https://github.com/flanksource/captain")
		document, err := resourceDocument("captain", resource.name, schema)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	return documents, nil
}
