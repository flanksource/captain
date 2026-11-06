package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/adapters/presentation"
)

func enrichRuntimeSchemas() {
	path := "pkg/adapters/openai/agent.schema.json"
	schema := readObject(path)
	definitions := map[string]any{}
	properties := schema["properties"].(map[string]any)
	for _, operation := range []struct{ name, file, native string }{
		{"initialize", "v1/InitializeParams", "initialize"},
		{"threadStart", "v2/ThreadStartParams", "thread/start"},
		{"threadResume", "v2/ThreadResumeParams", "thread/resume"},
		{"turnStart", "v2/TurnStartParams", "turn/start"},
	} {
		native := readObject(".tmp/adapter-schema-sources/codex-protocol/" + operation.file + ".json")
		renameReferences(native, "OpenAIAgent")
		for key, value := range native["definitions"].(map[string]any) {
			definitions["OpenAIAgent"+key] = value
		}
		old := properties[operation.name].(map[string]any)
		for key, raw := range native["properties"].(map[string]any) {
			field := raw.(map[string]any)
			previous, exists := old["properties"].(map[string]any)[key].(map[string]any)
			if exists {
				for _, annotation := range []string{"title", "description", "x-captain-option", "readOnly", "x-clicky-section", "x-clicky-order", "x-enum-display", "x-enum-icons"} {
					if value, ok := previous[annotation]; ok {
						field[annotation] = value
					}
				}
			} else {
				field["readOnly"] = true
				field["x-clicky-section"] = "cli"
				field["x-captain-option"] = map[string]any{"nativeName": operation.native + "." + key,
					"support": "unsupported", "note": "Accepted by the native protocol; Captain does not construct this option."}
			}
		}
		old["properties"] = native["properties"]
		delete(old, "required")
		if required, ok := native["required"]; ok {
			old["required"] = required
		}
	}
	schema["$defs"] = definitions
	sources := schema["x-captain-adapter"].(map[string]any)["sources"].([]any)
	for _, raw := range sources {
		source := raw.(map[string]any)
		if source["kind"] == "protocol" {
			source["version"] = "codex-cli 0.164.0"
		}
	}
	presentation.AnnotateSchema(schema, "https://learn.chatgpt.com/docs/developers/app-server")
	markNativeUnions(schema)
	writeObject(path, schema)
	enrichClaudeRuntime()
}

func enrichClaudeRuntime() {
	source := readObject("pkg/adapters/resources/anthropic.json")
	definitions := source["definitions"].(map[string]any)
	settings := definitions["Settings"].(map[string]any)
	path := "pkg/adapters/anthropic/agent.schema.json"
	schema := readObject(path)
	props := schema["properties"].(map[string]any)
	for _, mapping := range []struct{ property, definition string }{
		{"sandbox", ""}, {"settings", "Settings"}, {"managedSettings", "Settings"},
		{"agents", "AgentDefinition"}, {"mcpServers", "McpServerConfigForProcessTransport"},
	} {
		field := props[mapping.property].(map[string]any)
		var native map[string]any
		if mapping.property == "sandbox" {
			native = settings["properties"].(map[string]any)["sandbox"].(map[string]any)
		} else if mapping.property == "agents" || mapping.property == "mcpServers" {
			native = map[string]any{"type": "object", "additionalProperties": map[string]any{"$ref": "#/definitions/" + mapping.definition}}
		} else {
			native = map[string]any{"$ref": "#/definitions/" + mapping.definition}
		}
		delete(field, "additionalProperties")
		for key, value := range native {
			field[key] = value
		}
	}
	schema["definitions"] = definitions
	renameReferences(schema, "AnthropicAgent")
	renamed := map[string]any{}
	for key, value := range definitions {
		renamed["AnthropicAgent"+key] = value
	}
	delete(schema, "definitions")
	schema["$defs"] = renamed
	presentation.AnnotateSchema(schema, "https://platform.claude.com/docs/en/agent-sdk/typescript")
	markNativeUnions(schema)
	writeObject(path, schema)
}

func renameReferences(raw any, prefix string) {
	switch node := raw.(type) {
	case map[string]any:
		if ref, ok := node["$ref"].(string); ok && strings.HasPrefix(ref, "#/definitions/") {
			node["$ref"] = "#/$defs/" + prefix + strings.TrimPrefix(ref, "#/definitions/")
		}
		for _, child := range node {
			renameReferences(child, prefix)
		}
	case []any:
		for _, child := range node {
			renameReferences(child, prefix)
		}
	}
}

func markNativeUnions(raw any) {
	switch node := raw.(type) {
	case map[string]any:
		_, oneOf := node["oneOf"].([]any)
		_, anyOf := node["anyOf"].([]any)
		if oneOf || anyOf {
			node["goJSONSchema"] = map[string]any{"type": "json.RawMessage", "imports": []any{"encoding/json"}}
		}
		for _, child := range node {
			markNativeUnions(child)
		}
	case []any:
		for _, child := range node {
			markNativeUnions(child)
		}
	}
}

func readObject(path string) map[string]any {
	raw, err := os.ReadFile(path)
	must(err)
	var schema map[string]any
	must(json.Unmarshal(raw, &schema))
	return schema
}

func writeObject(path string, value map[string]any) {
	clearFieldIdentifiers(value)
	raw, err := json.MarshalIndent(value, "", "  ")
	must(err)
	must(os.WriteFile(path, append(raw, '\n'), 0o644))
	fmt.Println("Updated " + path)
}

func clearFieldIdentifiers(raw any) {
	switch node := raw.(type) {
	case map[string]any:
		if extension, ok := node["goJSONSchema"].(map[string]any); ok {
			delete(extension, "identifier")
			if len(extension) == 0 {
				delete(node, "goJSONSchema")
			}
		}
		for _, child := range node {
			clearFieldIdentifiers(child)
		}
	case []any:
		for _, child := range node {
			clearFieldIdentifiers(child)
		}
	}
}
