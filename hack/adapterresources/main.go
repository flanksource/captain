package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/adapters/presentation"
)

func main() {
	for _, source := range []struct{ provider, file, version, reference, docs string }{
		{"openai", "codex-config.json", "codex-cli 0.164.0", "https://raw.githubusercontent.com/openai/codex/rust-v0.164.0/codex-rs/core/config.schema.json", "https://learn.chatgpt.com/docs/config-file/config-reference"},
		{"anthropic", "claude-settings.json", "@anthropic-ai/claude-agent-sdk 0.3.280; ts-json-schema-generator 2.9.0", "@anthropic-ai/claude-agent-sdk/sdk.d.ts#Settings", "https://code.claude.com/docs/en/settings-reference"},
		{"google", "gemini-settings.json", "gemini-cli 0.64.0", "https://raw.githubusercontent.com/google-gemini/gemini-cli/v0.64.0/schemas/settings.schema.json", "https://geminicli.com/docs/reference/configuration/"},
	} {
		raw, err := os.ReadFile(".tmp/adapter-schema-sources/" + source.file)
		must(err)
		var schema map[string]any
		must(json.Unmarshal(raw, &schema))
		flattenRoot(schema)
		if source.provider == "openai" {
			completePermissionMaps(schema)
		}
		if source.provider == "anthropic" {
			mergeClaudeResources(schema)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok || len(properties) < 20 {
			panic(fmt.Sprintf("%s has no complete native configuration: %d properties", source.provider, len(properties)))
		}
		schema["title"] = strings.ToUpper(source.provider[:1]) + source.provider[1:] + " configuration"
		schema["description"] = "Complete native " + source.provider + " configuration captured from " + source.version + "."
		schema["x-captain-resource"] = map[string]any{
			"provider": source.provider, "version": source.version, "reference": source.reference,
			"sourceSha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "propertyCount": len(properties),
		}
		schema["x-captain-generated"] = "go run ./hack/adapterresources"
		presentation.AnnotateSchema(schema, source.docs)
		encoded, err := json.MarshalIndent(schema, "", "  ")
		must(err)
		must(os.WriteFile("pkg/adapters/resources/"+source.provider+".json", append(encoded, '\n'), 0o644))
		fmt.Printf("%s: %d top-level properties\n", source.provider, len(properties))
	}
	enrichRuntimeSchemas()
	enrichAPISchemas()
	annotateExecutionSchemas()
	inventoryCLIOptions()
}

func mergeClaudeResources(schema map[string]any) {
	definitions := schema["definitions"].(map[string]any)
	for _, resource := range []string{"mcp", "agent", "plugin"} {
		raw, err := os.ReadFile(".tmp/adapter-schema-sources/claude-" + resource + ".json")
		must(err)
		var extra map[string]any
		must(json.Unmarshal(raw, &extra))
		for key, value := range extra["definitions"].(map[string]any) {
			if existing, ok := definitions[key]; ok {
				left, err := json.Marshal(existing)
				must(err)
				right, err := json.Marshal(value)
				must(err)
				if string(left) != string(right) {
					panic("conflicting native definition " + key)
				}
			}
			definitions[key] = value
		}
	}
}

func flattenRoot(schema map[string]any) {
	for schema["$ref"] != nil {
		ref, ok := schema["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/definitions/") {
			panic(fmt.Sprintf("unexpected root reference %v", schema["$ref"]))
		}
		definitions := schema["definitions"].(map[string]any)
		target := definitions[strings.TrimPrefix(ref, "#/definitions/")].(map[string]any)
		delete(schema, "$ref")
		for key, value := range target {
			schema[key] = value
		}
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
